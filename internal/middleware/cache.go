// Package middleware exposes HTTP cross-cutting concerns. The cache helpers
// here are split out so main.go stays focused on wiring rather than carrying
// the per-prefix Cache-Control table inline.
package middleware

import (
	"encoding/hex"
	"strings"

	"github.com/cespare/xxhash/v2"
	"github.com/gofiber/fiber/v2"
)

// CacheDirective describes the Cache-Control variant we want to apply to a
// matched request. We compose the final header from these fields rather than
// passing a raw string so the per-prefix table stays scannable / diffable and
// so we can append `stale-if-error` uniformly.
type CacheDirective struct {
	// Visibility is "private" or "public". Per-user data must stay
	// "private" or a shared cache (CDN / reverse proxy) may fan out one
	// user's payload to others.
	Visibility string
	// MaxAge is the freshness window in seconds.
	MaxAge int
	// StaleWhileRevalidate gives clients a window to serve the cached copy
	// while asynchronously refreshing in the background.
	StaleWhileRevalidate int
	// StaleIfError lets clients keep serving the cached copy if the
	// backend is throwing 5xx; pairs with stale-while-revalidate.
	StaleIfError int
	// Public lets us suppress the `Vary: Authorization` header on
	// genuinely public responses (e.g. token-scoped invite preview).
	Public bool
}

// Header renders the directive as a Cache-Control header value.
func (d CacheDirective) Header() string {
	var b strings.Builder
	b.Grow(96)
	if d.Visibility != "" {
		b.WriteString(d.Visibility)
	} else {
		b.WriteString("private")
	}
	if d.MaxAge > 0 {
		b.WriteString(", max-age=")
		writeInt(&b, d.MaxAge)
	}
	if d.StaleWhileRevalidate > 0 {
		b.WriteString(", stale-while-revalidate=")
		writeInt(&b, d.StaleWhileRevalidate)
	}
	if d.StaleIfError > 0 {
		b.WriteString(", stale-if-error=")
		writeInt(&b, d.StaleIfError)
	}
	return b.String()
}

func writeInt(b *strings.Builder, v int) {
	// Small positive integers — avoid pulling in strconv just for this.
	if v == 0 {
		b.WriteByte('0')
		return
	}
	var buf [20]byte
	n := len(buf)
	for v > 0 {
		n--
		buf[n] = byte('0' + v%10)
		v /= 10
	}
	b.Write(buf[n:])
}

// cacheRule binds a path predicate to a CacheDirective. We evaluate rules in
// declaration order and the first match wins, so list the most-specific
// prefixes first.
type cacheRule struct {
	match     func(string) bool
	directive CacheDirective
}

// cacheRules is the per-endpoint Cache-Control table. The numbers come from
// the dashboard's actual access pattern (see HTTP-cost-reduction findings):
//
//   - /auth/me: hot path, small payload, read on every dashboard mount.
//   - list /budgets: changes whenever the user adds/removes a budget; modest
//     freshness window.
//   - single /budgets/:id: rarely renamed mid-session — slightly longer.
//   - /summary, /expenses: numbers move when the user logs an expense; keep
//     fresh.
//   - /trends: bucketed monthly figures; safe to hold for a minute.
//   - /budget-resume: historical close-out for past periods, very stable.
//   - /invites/:token: public, token IS the auth, no per-user variation.
var cacheRules = []cacheRule{
	{
		match: func(p string) bool { return strings.HasPrefix(p, "/api/auth/me") },
		directive: CacheDirective{
			Visibility: "private", MaxAge: 10,
			StaleWhileRevalidate: 60, StaleIfError: 300,
		},
	},
	{
		// /api/budgets/:id/budget-resume — historical, very stable.
		match: func(p string) bool { return matchBudgetSubpath(p, "budget-resume") },
		directive: CacheDirective{
			Visibility: "private", MaxAge: 300,
			StaleWhileRevalidate: 600, StaleIfError: 86400,
		},
	},
	{
		// /api/budgets/:id/trends — bucketed metrics, slow-moving.
		match: func(p string) bool { return matchBudgetSubpath(p, "trends") },
		directive: CacheDirective{
			Visibility: "private", MaxAge: 60,
			StaleWhileRevalidate: 300, StaleIfError: 86400,
		},
	},
	{
		// /api/budgets/:id/summary
		match: func(p string) bool { return matchBudgetSubpath(p, "summary") },
		directive: CacheDirective{
			Visibility: "private", MaxAge: 10,
			StaleWhileRevalidate: 60, StaleIfError: 300,
		},
	},
	{
		// /api/budgets/:id/expenses
		match: func(p string) bool { return matchBudgetSubpath(p, "expenses") },
		directive: CacheDirective{
			Visibility: "private", MaxAge: 10,
			StaleWhileRevalidate: 60, StaleIfError: 300,
		},
	},
	{
		// /api/budgets list (no trailing :id) and /api/budgets/:id (single).
		// We split single-budget reads from list reads via segment count.
		match: matchBudgetsList,
		directive: CacheDirective{
			Visibility: "private", MaxAge: 15,
			StaleWhileRevalidate: 60, StaleIfError: 300,
		},
	},
	{
		match: matchSingleBudget,
		directive: CacheDirective{
			Visibility: "private", MaxAge: 30,
			StaleWhileRevalidate: 120, StaleIfError: 300,
		},
	},
	{
		// Invite preview is public — the token itself is the credential.
		match: func(p string) bool {
			return strings.HasPrefix(p, "/api/invites/") &&
				!strings.Contains(p, "/accept")
		},
		directive: CacheDirective{
			Visibility: "public", MaxAge: 60,
			StaleIfError: 300, Public: true,
		},
	},
	{
		// Catch-all for any other /api/budgets/:id/foo (categories, links,
		// linkable, etc.) — keep the conservative default that was applied
		// before the per-endpoint refactor.
		match: func(p string) bool { return strings.HasPrefix(p, "/api/budgets") },
		directive: CacheDirective{
			Visibility: "private", MaxAge: 10,
			StaleWhileRevalidate: 30, StaleIfError: 300,
		},
	},
}

// matchBudgetSubpath returns true for /api/budgets/:id/<sub>.
func matchBudgetSubpath(path, sub string) bool {
	if !strings.HasPrefix(path, "/api/budgets/") {
		return false
	}
	rest := path[len("/api/budgets/"):]
	// We expect "<id>/<sub>" with no further trailing segments mattering.
	idx := strings.IndexByte(rest, '/')
	if idx <= 0 {
		return false
	}
	tail := rest[idx+1:]
	if tail == sub {
		return true
	}
	return strings.HasPrefix(tail, sub+"/") || strings.HasPrefix(tail, sub+"?")
}

// matchBudgetsList returns true for the bare /api/budgets or /api/budgets/.
func matchBudgetsList(path string) bool {
	return path == "/api/budgets" || path == "/api/budgets/"
}

// matchSingleBudget returns true for /api/budgets/:id (no further subpath).
func matchSingleBudget(path string) bool {
	if !strings.HasPrefix(path, "/api/budgets/") {
		return false
	}
	rest := path[len("/api/budgets/"):]
	if rest == "" {
		return false
	}
	// No further slash → it's the single-budget endpoint.
	return strings.IndexByte(rest, '/') == -1
}

// LookupCacheDirective returns the matching directive (and ok=true) for a
// request path or zero/false if the path is not eligible for soft caching.
//
// Mutation-heavy access-control subpaths (invites list, collaborators) are
// excluded here so the client never serves stale ACL data.
func LookupCacheDirective(path string) (CacheDirective, bool) {
	if strings.Contains(path, "/invites") && strings.HasPrefix(path, "/api/budgets") {
		return CacheDirective{}, false
	}
	if strings.Contains(path, "/collaborators") {
		return CacheDirective{}, false
	}
	for _, r := range cacheRules {
		if r.match(path) {
			return r.directive, true
		}
	}
	return CacheDirective{}, false
}

// IfNoneMatchHas reports whether the supplied If-None-Match header value
// contains the given ETag. It tolerates the common formats:
//
//   - single tag: `W/"abc"` or `"abc"`
//   - comma-separated list with optional spaces: `W/"a", W/"b"`
//   - the wildcard form `*` (matches anything we'd otherwise emit)
//
// The comparison is byte-exact on the ETag value the caller provides. Callers
// should pass our weak ETag (e.g. `W/"…"`) so the header round-trips cleanly.
func IfNoneMatchHas(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	for {
		comma := indexUnquotedComma(header)
		var item string
		if comma < 0 {
			item = header
		} else {
			item = header[:comma]
		}
		if strings.TrimSpace(item) == etag {
			return true
		}
		if comma < 0 {
			return false
		}
		header = header[comma+1:]
	}
}

// indexUnquotedComma returns the index of the first comma not inside a quoted
// section, or -1. A bare ETag value can never contain an unescaped comma so
// the parser only needs to track quote state, not handle backslash escapes.
func indexUnquotedComma(s string) int {
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				return i
			}
		}
	}
	return -1
}

// ComputeETag returns a weak ETag for the given response body. We use
// xxhash/v2 (a 64-bit non-cryptographic hash) because:
//
//   - the ETag is only used for equality testing, not security; an attacker
//     who can flip the response body can also flip the ETag.
//   - 16-hex-char output is half the size of the previous SHA-256 prefix;
//     for tens-of-thousands of conditional requests per day that adds up.
//   - xxhash hashes ~10 GB/s on modern hardware, so the per-response cost
//     is in low single-digit microseconds for our typical 30-200 KB JSON.
func ComputeETag(body []byte) string {
	sum := xxhash.Sum64(body)
	var buf [8]byte
	buf[0] = byte(sum >> 56)
	buf[1] = byte(sum >> 48)
	buf[2] = byte(sum >> 40)
	buf[3] = byte(sum >> 32)
	buf[4] = byte(sum >> 24)
	buf[5] = byte(sum >> 16)
	buf[6] = byte(sum >> 8)
	buf[7] = byte(sum)
	return `W/"` + hex.EncodeToString(buf[:]) + `"`
}

// CacheControlAndETag is the per-prefix Cache-Control + ETag middleware.
//
// PERF: short-circuits to 304 Not Modified on If-None-Match hits so the
// dashboard's repeated polls only pay a header round-trip rather than the
// full 30-200 KB JSON payload.
//
// SECURITY: `private` keeps user-scoped payloads out of shared caches.
// `Vary: Authorization` forces a per-token cache key so a shared cache
// cannot serve user A's response to user B. `Vary: Accept-Encoding` keeps
// brotli/gzip/identity copies disambiguated for any intermediate cache.
func CacheControlAndETag() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Method() != fiber.MethodGet {
			return c.Next()
		}
		directive, ok := LookupCacheDirective(c.Path())
		if !ok {
			return c.Next()
		}

		// Let the downstream handler run and fill in the body.
		if err := c.Next(); err != nil {
			return err
		}

		status := c.Response().StatusCode()
		if status < 200 || status >= 300 {
			return nil
		}

		body := c.Response().Body()
		if len(body) == 0 {
			return nil
		}

		// PERF: cap ETag computation at 256KB. Defense in depth: future
		// changes that accidentally stream large payloads through this
		// path won't block the event loop on hashing megabytes.
		const maxETagBody = 256 * 1024
		if len(body) > maxETagBody {
			return nil
		}

		c.Set("Cache-Control", directive.Header())
		// Public endpoints don't need to vary on Authorization (the token
		// is part of the path); private endpoints must, so a CDN/proxy
		// cannot cross-pollinate sessions.
		if directive.Public {
			c.Set("Vary", "Accept-Encoding")
		} else {
			c.Set("Vary", "Authorization, Accept-Encoding")
		}

		etag := ComputeETag(body)
		c.Set("ETag", etag)

		if inm := c.Get("If-None-Match"); inm != "" && IfNoneMatchHas(inm, etag) {
			c.Status(fiber.StatusNotModified)
			c.Response().ResetBody()
			c.Response().Header.Del("Content-Length")
			return nil
		}
		return nil
	}
}
