package captcha

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestTurnstile_HappyPath verifies that a well-formed siteverify response
// with success=true results in a nil error from Verify.
func TestTurnstile_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	v := newTurnstileWith("sekret", server.URL, server.Client())
	if err := v.Verify(context.Background(), "token-1", "127.0.0.1", "login"); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

// TestTurnstile_FailureModes covers every documented siteverify failure
// signal — they must all return ErrCaptchaFailed via errors.Is.
func TestTurnstile_FailureModes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		statusCode int
		body       string
	}{
		{"success_false", 200, `{"success":false,"error-codes":["invalid-input-response"]}`},
		{"success_false_no_codes", 200, `{"success":false}`},
		{"non_200_status", 400, `{"success":false,"error-codes":["bad-request"]}`},
		{"non_200_server_error", 502, `bad gateway`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			v := newTurnstileWith("sekret", server.URL, server.Client())
			err := v.Verify(context.Background(), "token", "1.2.3.4", "login")
			if err == nil {
				t.Fatalf("Verify() = nil, want error")
			}
			if !errors.Is(err, ErrCaptchaFailed) {
				t.Fatalf("Verify() = %v, want errors.Is ErrCaptchaFailed", err)
			}
		})
	}
}

// TestTurnstile_ActionMismatch verifies that a Cloudflare-echoed action
// different from the caller-supplied action is treated as bot.
func TestTurnstile_ActionMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"action":"register"}`))
	}))
	defer server.Close()

	v := newTurnstileWith("sekret", server.URL, server.Client())
	err := v.Verify(context.Background(), "tok", "", "login")
	if err == nil || !errors.Is(err, ErrCaptchaFailed) {
		t.Fatalf("Verify() = %v, want bot error", err)
	}
}

// TestTurnstile_ActionEchoed verifies that a matching action is accepted.
func TestTurnstile_ActionEchoed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		action := r.PostForm.Get("action")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"action":"` + action + `"}`))
	}))
	defer server.Close()

	v := newTurnstileWith("sekret", server.URL, server.Client())
	if err := v.Verify(context.Background(), "tok", "", "login"); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

// TestTurnstile_MissingToken returns ErrMissingToken without hitting the
// network — the middleware translates that into a 400.
func TestTurnstile_MissingToken(t *testing.T) {
	v := newTurnstileWith("sekret", "http://localhost:0", http.DefaultClient)
	err := v.Verify(context.Background(), "", "", "")
	if !errors.Is(err, ErrMissingToken) {
		t.Fatalf("Verify() = %v, want ErrMissingToken", err)
	}
}

// TestTurnstile_CacheHitAvoidsSecondCall verifies that re-presenting the
// same token within 5 minutes does not hit Cloudflare a second time.
//
// PERF: this is the key replay-absorbing behaviour. Cloudflare allows
// exactly one siteverify per token; the cache prevents a client retry
// from accidentally invalidating its own session.
func TestTurnstile_CacheHitAvoidsSecondCall(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	v := newTurnstileWith("sekret", server.URL, server.Client())
	for i := 0; i < 5; i++ {
		if err := v.Verify(context.Background(), "tok-replay", "", ""); err != nil {
			t.Fatalf("attempt %d Verify() = %v", i, err)
		}
	}
	if hits != 1 {
		t.Fatalf("upstream hits = %d, want exactly 1 (cache should absorb retries)", hits)
	}
	if got := v.cacheSize(); got != 1 {
		t.Fatalf("cache size = %d, want 1", got)
	}
}

// TestTurnstile_FailuresAreNotCached verifies that a failed verification
// is not cached — a subsequent identical token call must re-hit Cloudflare.
func TestTurnstile_FailuresAreNotCached(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
	}))
	defer server.Close()

	v := newTurnstileWith("sekret", server.URL, server.Client())
	for i := 0; i < 3; i++ {
		err := v.Verify(context.Background(), "tok-fail", "", "")
		if !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("attempt %d Verify() = %v, want ErrCaptchaFailed", i, err)
		}
	}
	if hits != 3 {
		t.Fatalf("upstream hits = %d, want 3 (failures must not cache)", hits)
	}
}

// TestTurnstile_CacheEvictionFIFO verifies the FIFO eviction kicks in
// when more than turnstileCacheMaxEntries entries accumulate. We don't
// hit the prod limit (10k) in the test — instead we exercise the path by
// pre-populating beyond the configured size and re-inserting.
func TestTurnstile_CacheEvictionFIFO(t *testing.T) {
	v := newTurnstileWith("sekret", "http://localhost:0", http.DefaultClient)
	// Stuff the cache below the cap, then push past it.
	for i := 0; i < turnstileCacheMaxEntries+5; i++ {
		v.cacheStore("k" + paddedHex(i))
	}
	if got := v.cacheSize(); got > turnstileCacheMaxEntries {
		t.Fatalf("cache size = %d, want <= %d", got, turnstileCacheMaxEntries)
	}
}

// TestTurnstile_CacheExpiry verifies that the TTL is honoured — an entry
// older than turnstileCacheTTL no longer counts as a hit.
func TestTurnstile_CacheExpiry(t *testing.T) {
	v := newTurnstileWith("sekret", "http://localhost:0", http.DefaultClient)
	v.cacheStore("k1")
	// Backdate the entry.
	v.mu.Lock()
	v.cache["k1"] = turnstileCacheEntry{verifiedAt: time.Now().Add(-2 * turnstileCacheTTL)}
	v.mu.Unlock()
	if v.cacheHit("k1") {
		t.Fatal("cacheHit returned true for expired entry")
	}
}

// TestTurnstile_PostFormEncodesSecretAndToken validates that the verifier
// sends secret + response + remoteip in the form body. Catches accidental
// regressions to query-string encoding or missing fields.
func TestTurnstile_PostFormEncodesSecretAndToken(t *testing.T) {
	var got struct {
		secret, response, remoteip, action string
		contentType                        string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.contentType = r.Header.Get("Content-Type")
		_ = r.ParseForm()
		got.secret = r.PostForm.Get("secret")
		got.response = r.PostForm.Get("response")
		got.remoteip = r.PostForm.Get("remoteip")
		got.action = r.PostForm.Get("action")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer server.Close()

	v := newTurnstileWith("the-secret", server.URL, server.Client())
	_ = v.Verify(context.Background(), "the-token", "9.9.9.9", "login")

	if got.secret != "the-secret" {
		t.Errorf("secret = %q, want %q", got.secret, "the-secret")
	}
	if got.response != "the-token" {
		t.Errorf("response = %q, want %q", got.response, "the-token")
	}
	if got.remoteip != "9.9.9.9" {
		t.Errorf("remoteip = %q, want %q", got.remoteip, "9.9.9.9")
	}
	if got.action != "login" {
		t.Errorf("action = %q, want %q", got.action, "login")
	}
	if !strings.HasPrefix(got.contentType, "application/x-www-form-urlencoded") {
		t.Errorf("Content-Type = %q, want form", got.contentType)
	}
}

// TestNewTurnstile_NoopOnEmptySecret verifies the dev/staging fail-open
// path returns a NoOp verifier whose Verify always succeeds.
func TestNewTurnstile_NoopOnEmptySecret(t *testing.T) {
	v := NewTurnstile("")
	if Reason(v) == "" {
		t.Fatal("Reason should be non-empty for a noop verifier")
	}
	if err := v.Verify(context.Background(), "any-token", "1.1.1.1", "x"); err != nil {
		t.Fatalf("noop Verify() = %v, want nil", err)
	}
}

// TestNewTurnstile_NotNoopWhenSecretPresent guards against accidentally
// returning a noop when the secret is set.
func TestNewTurnstile_NotNoopWhenSecretPresent(t *testing.T) {
	v := NewTurnstile("the-secret")
	if Reason(v) != "" {
		t.Fatal("Reason should be empty for a non-noop verifier")
	}
}

// paddedHex makes a 6-char hex string from an int, used to build distinct
// cache keys without sprinkling fmt.Sprintf.
func paddedHex(i int) string {
	const hexd = "0123456789abcdef"
	b := make([]byte, 6)
	for j := 5; j >= 0; j-- {
		b[j] = hexd[i&0xf]
		i >>= 4
	}
	return string(b)
}
