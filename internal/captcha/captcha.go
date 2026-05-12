// Package captcha contains bot-protection verifiers for the public auth
// surface. Three signal sources are supported:
//
//   - Cloudflare Turnstile (web — siteverify round-trip).
//   - Apple App Attest (iOS — local ECDSA P-256 signature verify against a
//     pre-registered attestation public key).
//   - Google Play Integrity (Android — local JWE decrypt + JWS verify against
//     project keys).
//
// Each implementation satisfies the Verifier interface. NewMulti dispatches by
// the X-App-Platform header so a single middleware can handle all three.
//
// SECURITY: every verifier returns ErrCaptchaFailed on bot detection. The
// middleware translates that into a 403 with the BOT_DETECTED code. Other
// errors (network, parse) are returned verbatim so operators can distinguish
// "tried and failed verification" from "couldn't reach Cloudflare". The
// middleware logs both but only the former blocks the request — the latter
// is also blocked (fail-closed) so a Turnstile outage cannot become a
// captcha bypass.
//
// PERF: Turnstile siteverify is the only network round-trip. We cache
// successful validations by SHA256(token) for 5 minutes so client-side
// retries on transient failures don't burn the one-shot Turnstile token.
// App Attest + Play Integrity are local crypto, sub-millisecond.
package captcha

import (
	"context"
	"errors"
)

// ErrCaptchaFailed is returned by Verify when bot detection trips. The
// middleware maps it to a 403 + {error: "captcha_failed", code: "BOT_DETECTED"}.
//
// Operators distinguishing legitimate failures from transient infrastructure
// errors (network, decode) should check for this sentinel — anything else is
// likely a backend problem worth alerting on.
var ErrCaptchaFailed = errors.New("captcha verification failed")

// ErrMissingToken is returned when the caller did not present a captcha token.
// Middleware maps it to a 400.
var ErrMissingToken = errors.New("captcha token missing")

// Verifier is the single abstraction all platform implementations satisfy.
//
// The action string is an optional binding — Turnstile accepts a per-form
// action to scope tokens, the App Attest server-side challenge is derived
// from it. Implementations that don't use it simply ignore it.
type Verifier interface {
	Verify(ctx context.Context, token, remoteIP, action string) error
}

// noop is a pass-through Verifier used when the relevant env var is empty.
// Dev / staging environments without secrets keep working; main.go logs a
// warning at startup so prod operators don't ship to production blind.
type noop struct{ reason string }

// Verify always succeeds for the no-op verifier.
func (noop) Verify(_ context.Context, _, _, _ string) error { return nil }

// NewNoop returns a Verifier that accepts every request. The reason is
// surfaced in startup logs.
//
// SECURITY: only ever returned when the operator has explicitly left a
// platform's secret unset. Never wire this up unconditionally on a route.
func NewNoop(reason string) Verifier { return noop{reason: reason} }

// Reason returns the operator-facing explanation embedded in a no-op verifier
// (e.g. "TURNSTILE_SECRET empty"). Returns "" for any non-noop verifier.
//
// Used by main.go to log a single startup warning per disabled platform.
func Reason(v Verifier) string {
	if n, ok := v.(noop); ok {
		return n.reason
	}
	return ""
}
