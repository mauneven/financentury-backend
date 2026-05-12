package captcha

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// turnstileSiteverifyURL is Cloudflare's siteverify endpoint. Overridable in
// tests by injecting a different HTTPClient + base URL via newTurnstileWith.
const turnstileSiteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// turnstileMaxResponseBytes caps the siteverify response body. Cloudflare's
// JSON payload is < 1 KB; the cap defends against a misbehaving / hostile
// upstream serving an unbounded stream.
const turnstileMaxResponseBytes = 16 * 1024

// turnstileHTTPTimeout bounds each siteverify call. Cloudflare's p99 is well
// under a second; anything past 5s is a DoS amplifier.
const turnstileHTTPTimeout = 5 * time.Second

// turnstileCacheTTL is how long a successful validation is reused. Cloudflare
// only allows ONE successful siteverify per token: if the client retries with
// the same token (transient network failure, double-submit) we MUST re-use
// the cached "ok" instead of asking Cloudflare again, which would 400 with
// "timeout-or-duplicate" and 403 a legitimate user.
const turnstileCacheTTL = 5 * time.Minute

// turnstileCacheMaxEntries caps the cache so a flood of distinct tokens
// can't OOM the process. Each entry is ~80 bytes (sha256 hex + timestamp +
// pointer overhead); 10k entries ≈ 1 MB. Eviction is FIFO via the evictor.
const turnstileCacheMaxEntries = 10_000

// turnstileResponse mirrors Cloudflare's siteverify JSON shape.
//
// Fields beyond Success / ErrorCodes are decoded for diagnostic logging but
// are not load-bearing for the bot/non-bot decision.
type turnstileResponse struct {
	Success     bool     `json:"success"`
	ErrorCodes  []string `json:"error-codes"`
	ChallengeTS string   `json:"challenge_ts"`
	Hostname    string   `json:"hostname"`
	Action      string   `json:"action"`
	CData       string   `json:"cdata"`
}

// turnstileCacheEntry tracks when a token was last verified successfully.
type turnstileCacheEntry struct {
	verifiedAt time.Time
}

// turnstileVerifier implements Verifier against Cloudflare's siteverify API.
type turnstileVerifier struct {
	secretKey  string
	endpoint   string
	httpClient *http.Client

	mu       sync.Mutex
	cache    map[string]turnstileCacheEntry
	cacheLRU []string // insertion-order key list for FIFO eviction
}

// NewTurnstile returns a Verifier that validates Cloudflare Turnstile tokens.
//
// When secretKey is empty, returns a NoOp — dev / staging without secrets
// keeps working, main.go logs a warning so prod operators notice. The cache
// is sized to absorb realistic retry storms (sub-RPS per token, 5min TTL)
// without unbounded growth (see turnstileCacheMaxEntries).
func NewTurnstile(secretKey string) Verifier {
	if strings.TrimSpace(secretKey) == "" {
		return NewNoop("TURNSTILE_SECRET empty (Cloudflare Turnstile disabled)")
	}
	return newTurnstileWith(secretKey, turnstileSiteverifyURL, &http.Client{
		Timeout: turnstileHTTPTimeout,
	})
}

// newTurnstileWith is the test seam: it lets tests inject a custom HTTP
// client + base URL (httptest.Server) without changing NewTurnstile's
// production-safe signature.
func newTurnstileWith(secretKey, endpoint string, client *http.Client) *turnstileVerifier {
	return &turnstileVerifier{
		secretKey:  secretKey,
		endpoint:   endpoint,
		httpClient: client,
		cache:      make(map[string]turnstileCacheEntry, 256),
	}
}

// Verify validates the Turnstile token against Cloudflare's siteverify.
// Successful validations are cached by SHA256(token) for turnstileCacheTTL.
//
// Returns ErrCaptchaFailed on any of:
//   - empty token
//   - Cloudflare returns success=false
//   - Cloudflare returns non-empty error-codes
//
// Returns a non-nil non-sentinel error for transport / decode failures so
// the middleware can fail-closed and operators can distinguish bot signal
// from infrastructure breakage.
func (t *turnstileVerifier) Verify(ctx context.Context, token, remoteIP, action string) error {
	if token == "" {
		return ErrMissingToken
	}

	// SECURITY: cache key is sha256(token). Tokens are short-lived but
	// non-trivially long; hashing avoids retaining the raw token in memory
	// where a heap dump could leak it.
	h := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(h[:])

	if t.cacheHit(key) {
		return nil
	}

	form := url.Values{}
	form.Set("secret", t.secretKey)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	if action != "" {
		// SECURITY: scoping a token to an action (set client-side via
		// turnstile.render({action: 'login'})) lets us reject tokens that
		// were issued for a different surface. Cloudflare echoes the action
		// back in the response; comparison happens below.
		form.Set("action", action)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("turnstile: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("turnstile: post siteverify: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// SECURITY: any non-200 from Cloudflare is treated as bot — they
		// only emit 200 on a well-formed request, even when validation
		// fails (success=false comes back in the JSON body). 4xx / 5xx
		// indicate a broken caller (wrong secret, malformed body) or a
		// Cloudflare outage; both should fail-closed.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, turnstileMaxResponseBytes))
		return fmt.Errorf("turnstile: siteverify status=%d body=%s: %w", resp.StatusCode, string(body), ErrCaptchaFailed)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, turnstileMaxResponseBytes))
	if err != nil {
		return fmt.Errorf("turnstile: read body: %w", err)
	}

	var decoded turnstileResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return fmt.Errorf("turnstile: decode body: %w", err)
	}

	if !decoded.Success {
		// Wrap so callers can distinguish "Cloudflare flagged it" via
		// errors.Is(err, ErrCaptchaFailed) while still seeing error-codes
		// in logs.
		return fmt.Errorf("turnstile: rejected codes=%v: %w", decoded.ErrorCodes, ErrCaptchaFailed)
	}

	// Optional action binding. If the caller asked for a specific action
	// and Cloudflare echoed back a different one, treat as bot — token was
	// minted for the wrong surface.
	if action != "" && decoded.Action != "" && decoded.Action != action {
		return fmt.Errorf("turnstile: action mismatch want=%q got=%q: %w", action, decoded.Action, ErrCaptchaFailed)
	}

	t.cacheStore(key)
	return nil
}

// cacheHit returns true if the token-hash was verified within the TTL.
// Returns false on miss OR expiry; the latter is deleted lazily.
func (t *turnstileVerifier) cacheHit(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	entry, ok := t.cache[key]
	if !ok {
		return false
	}
	if time.Since(entry.verifiedAt) > turnstileCacheTTL {
		delete(t.cache, key)
		return false
	}
	return true
}

// cacheStore inserts a successful validation. FIFO-evicts the oldest entry
// when over capacity.
func (t *turnstileVerifier) cacheStore(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.cache[key]; ok {
		// Already cached — refresh timestamp but don't re-append to the LRU
		// list (would create a duplicate, breaking eviction).
		t.cache[key] = turnstileCacheEntry{verifiedAt: time.Now()}
		return
	}
	if len(t.cache) >= turnstileCacheMaxEntries {
		// Evict the oldest insertion. We tolerate the occasional missed
		// hit (caller will re-verify) in exchange for an O(1) data
		// structure that doesn't need a heap.
		if len(t.cacheLRU) > 0 {
			oldest := t.cacheLRU[0]
			t.cacheLRU = t.cacheLRU[1:]
			delete(t.cache, oldest)
		}
	}
	t.cache[key] = turnstileCacheEntry{verifiedAt: time.Now()}
	t.cacheLRU = append(t.cacheLRU, key)
}

// cacheSize returns the current entry count (test seam).
func (t *turnstileVerifier) cacheSize() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.cache)
}
