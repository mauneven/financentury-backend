package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
)

// TestLookupCacheDirective_PerEndpoint asserts that each endpoint resolves
// to the right Cache-Control directive. The expected values mirror the
// per-endpoint table in cache.go and the cost-reduction spec.
func TestLookupCacheDirective_PerEndpoint(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		wantOK bool
		want   string
		public bool
	}{
		{
			name:   "auth/me",
			path:   "/api/auth/me",
			wantOK: true,
			want:   "private, max-age=10, stale-while-revalidate=60, stale-if-error=300",
		},
		{
			name:   "budgets list",
			path:   "/api/budgets",
			wantOK: true,
			want:   "private, max-age=15, stale-while-revalidate=60, stale-if-error=300",
		},
		{
			name:   "budgets list trailing slash",
			path:   "/api/budgets/",
			wantOK: true,
			want:   "private, max-age=15, stale-while-revalidate=60, stale-if-error=300",
		},
		{
			name:   "single budget",
			path:   "/api/budgets/abc",
			wantOK: true,
			want:   "private, max-age=30, stale-while-revalidate=120, stale-if-error=300",
		},
		{
			name:   "summary",
			path:   "/api/budgets/abc/summary",
			wantOK: true,
			want:   "private, max-age=10, stale-while-revalidate=60, stale-if-error=300",
		},
		{
			name:   "trends",
			path:   "/api/budgets/abc/trends",
			wantOK: true,
			want:   "private, max-age=60, stale-while-revalidate=300, stale-if-error=86400",
		},
		{
			name:   "budget-resume",
			path:   "/api/budgets/abc/budget-resume",
			wantOK: true,
			want:   "private, max-age=300, stale-while-revalidate=600, stale-if-error=86400",
		},
		{
			name:   "expenses",
			path:   "/api/budgets/abc/expenses",
			wantOK: true,
			want:   "private, max-age=10, stale-while-revalidate=60, stale-if-error=300",
		},
		{
			name:   "categories falls back to default",
			path:   "/api/budgets/abc/categories",
			wantOK: true,
			want:   "private, max-age=10, stale-while-revalidate=30, stale-if-error=300",
		},
		{
			name:   "public invite preview",
			path:   "/api/invites/some-token",
			wantOK: true,
			want:   "public, max-age=60, stale-if-error=300",
			public: true,
		},
		{
			name:   "invites list excluded",
			path:   "/api/budgets/abc/invites",
			wantOK: false,
		},
		{
			name:   "collaborators excluded",
			path:   "/api/budgets/abc/collaborators",
			wantOK: false,
		},
		{
			name:   "non-API path",
			path:   "/health",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := LookupCacheDirective(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("LookupCacheDirective(%q) ok = %v, want %v", tc.path, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if got := d.Header(); got != tc.want {
				t.Errorf("Header() = %q, want %q", got, tc.want)
			}
			if d.Public != tc.public {
				t.Errorf("Public = %v, want %v", d.Public, tc.public)
			}
		})
	}
}

// TestComputeETag_ShortFormStable verifies the xxhash64-derived ETag is the
// expected weak / 16-hex-char shape and is stable across calls for the same
// input but different across distinct inputs.
func TestComputeETag_ShortFormStable(t *testing.T) {
	body := []byte(`{"income":100,"expenses":42}`)
	a := ComputeETag(body)
	b := ComputeETag(body)
	if a != b {
		t.Errorf("ETag is not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, `W/"`) || !strings.HasSuffix(a, `"`) {
		t.Errorf("ETag %q is not a weak quoted form", a)
	}
	// Body of `W/"` (3) + 16 hex chars + `"` (1) = 20 chars.
	if len(a) != 20 {
		t.Errorf("ETag len = %d (%q), want 20", len(a), a)
	}
	other := ComputeETag([]byte(`{"income":100,"expenses":43}`))
	if other == a {
		t.Errorf("expected distinct ETag for distinct body, got %q for both", a)
	}
}

// TestIfNoneMatchHas_Variants exercises the `, `-separated parser, the
// wildcard, and quote-aware splitting so we don't accidentally split mid-tag.
func TestIfNoneMatchHas_Variants(t *testing.T) {
	const target = `W/"abc123"`
	cases := []struct {
		header string
		want   bool
	}{
		{header: "", want: false},
		{header: target, want: true},
		{header: `   ` + target + `   `, want: true},
		{header: `W/"other", ` + target, want: true},
		{header: `W/"other",` + target, want: true},
		{header: `W/"other"  ,  ` + target, want: true},
		{header: target + `, W/"another"`, want: true},
		{header: `W/"a", W/"b", W/"c"`, want: false},
		{header: `*`, want: true},
		// A quoted comma must not split: ensure we don't split inside a
		// hypothetical etag that contained a comma byte. Our ETags never
		// do, but the parser should be robust to it.
		{header: `W/"foo,bar", ` + target, want: true},
	}
	for _, tc := range cases {
		got := IfNoneMatchHas(tc.header, target)
		if got != tc.want {
			t.Errorf("IfNoneMatchHas(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

// TestCacheControlAndETag_StaleIfErrorPresent confirms that every soft-
// cacheable endpoint emits the stale-if-error directive (so a transient
// backend hiccup doesn't blank out the dashboard).
func TestCacheControlAndETag_StaleIfErrorPresent(t *testing.T) {
	for _, path := range []string{
		"/api/auth/me",
		"/api/budgets",
		"/api/budgets/abc",
		"/api/budgets/abc/summary",
		"/api/budgets/abc/trends",
		"/api/budgets/abc/budget-resume",
		"/api/budgets/abc/expenses",
	} {
		d, ok := LookupCacheDirective(path)
		if !ok {
			t.Errorf("%s: not eligible (unexpected)", path)
			continue
		}
		if d.StaleIfError <= 0 {
			t.Errorf("%s: missing stale-if-error", path)
		}
		if !strings.Contains(d.Header(), "stale-if-error=") {
			t.Errorf("%s: header %q missing stale-if-error", path, d.Header())
		}
	}
}

// TestCacheControlAndETag_PublicInviteOmitsAuthVary confirms that the public
// invite preview does NOT vary on Authorization (only Accept-Encoding) so
// CDNs can cache the same response across users.
func TestCacheControlAndETag_PublicInviteOmitsAuthVary(t *testing.T) {
	app := fiber.New()
	app.Use(CacheControlAndETag())
	app.Get("/api/invites/:token", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"budgetName": "x", "inviterName": "y"})
	})
	req := httptest.NewRequest(http.MethodGet, "/api/invites/sometoken", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if cc := resp.Header.Get("Cache-Control"); !strings.HasPrefix(cc, "public,") {
		t.Errorf("Cache-Control = %q, want public visibility", cc)
	}
	if vary := resp.Header.Get("Vary"); vary != "Accept-Encoding" {
		t.Errorf("Vary = %q, want %q", vary, "Accept-Encoding")
	}
}

// TestCompressNegotiation_BrotliPreferred mounts the same compress
// middleware used in main.go and asserts that:
//   - Accept-Encoding: br, gzip yields a brotli-encoded response (Content-
//     Encoding: br) that decodes to the original JSON.
//   - Accept-Encoding: gzip alone yields a gzip-encoded response that
//     decodes to the original JSON.
//   - No Accept-Encoding yields an identity response.
//
// This guards against a regression where Fiber/fasthttp drops brotli support
// (e.g. via a compression library swap) and silently falls back to gzip.
func TestCompressNegotiation_BrotliPreferred(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(compress.New(compress.Config{Level: compress.LevelBestSpeed}))
	// Body that is large + repetitive enough to be worth compressing.
	// fasthttp skips compression below a small threshold.
	const filler = `{"a":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	app.Get("/x", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "application/json")
		return c.SendString(strings.Repeat(filler, 50))
	})

	t.Run("brotli", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Accept-Encoding", "br, gzip")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if got := resp.Header.Get("Content-Encoding"); got != "br" {
			t.Fatalf("Content-Encoding = %q, want %q", got, "br")
		}
		raw, _ := io.ReadAll(resp.Body)
		decoded, err := io.ReadAll(brotli.NewReader(strings.NewReader(string(raw))))
		if err != nil {
			t.Fatalf("brotli decode failed: %v", err)
		}
		if !strings.Contains(string(decoded), filler) {
			t.Errorf("decoded payload missing expected filler")
		}
	})

	t.Run("gzip-fallback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want %q", got, "gzip")
		}
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		decoded, err := io.ReadAll(gz)
		if err != nil {
			t.Fatalf("gzip decode failed: %v", err)
		}
		if !strings.Contains(string(decoded), filler) {
			t.Errorf("decoded payload missing expected filler")
		}
	})

	t.Run("identity", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		// No Accept-Encoding → identity.
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if got := resp.Header.Get("Content-Encoding"); got != "" {
			t.Errorf("Content-Encoding = %q, want empty (identity)", got)
		}
	})
}

// TestCompressNegotiation_BrotliBeatsGzip ensures brotli actually produces a
// smaller payload than gzip for our typical JSON shape, which is the whole
// point of preferring it. We only assert directional inequality; the exact
// ratio depends on the underlying lib version.
func TestCompressNegotiation_BrotliBeatsGzip(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(compress.New(compress.Config{Level: compress.LevelBestSpeed}))
	// Repetitive JSON (similar to /summary or /trends shape).
	const item = `{"id":"abc-def-1234","amount":1234.56,"category":"groceries","date":"2026-05-09"},`
	body := `[` + strings.TrimSuffix(strings.Repeat(item, 200), `,`) + `]`
	app.Get("/y", func(c *fiber.Ctx) error {
		c.Set("Content-Type", "application/json")
		return c.SendString(body)
	})

	doRequest := func(enc string) int {
		req := httptest.NewRequest(http.MethodGet, "/y", nil)
		req.Header.Set("Accept-Encoding", enc)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return len(raw)
	}

	brSize := doRequest("br")
	gzSize := doRequest("gzip")
	if brSize == 0 || gzSize == 0 {
		t.Fatalf("missing compressed body sizes: br=%d gzip=%d", brSize, gzSize)
	}
	if brSize >= gzSize {
		t.Errorf("expected brotli < gzip; got br=%d gzip=%d", brSize, gzSize)
	}
	t.Logf("compressed sizes: br=%d gzip=%d (savings = %d bytes, %.1f%%)",
		brSize, gzSize, gzSize-brSize, 100.0*float64(gzSize-brSize)/float64(gzSize))
}
