// Package middleware: captcha bot-protection guard.
//
// # SECURITY MODEL
//
// Public auth endpoints (Google OAuth exchange, email login/register,
// invite preview) are the brute-force / abuse surface. We protect them
// with a single Captcha() middleware that:
//
//  1. Reads X-Captcha-Token. Absent → 400 (caller bug, not bot).
//  2. Reads X-App-Platform (web | ios | android). Absent → assumes web.
//  3. Dispatches to the configured platform Verifier.
//  4. On success: stamps c.Locals("captcha_verified", true) and continues.
//  5. On failure: returns 403 with {error, code: BOT_DETECTED}.
//
// Failure-mode budget:
//
//   - Token missing → 400. Not a bot signal — the client just forgot to
//     attach it. We don't burn the bot budget.
//   - Verifier returns errors.Is(err, captcha.ErrCaptchaFailed) → 403.
//   - Verifier returns any other error (network, decode, config) → 403.
//     Fail-closed. A Cloudflare outage temporarily breaks auth; that's
//     intentional. The alternative is a global captcha bypass on Cloudflare
//     failure, which is strictly worse.
//
// Operators who need to ship without captcha (dev / staging / disaster
// recovery) leave the platform secret unset. captcha.New*() returns a
// no-op verifier in that case and main.go logs a startup warning so the
// state is observable.
package middleware

import (
	"errors"
	"log"

	"github.com/gofiber/fiber/v2"

	"github.com/the-financial-workspace/backend/internal/captcha"
)

// Captcha-related header names. Documented up here so route handlers (or
// integration tests) can reference them without hard-coding strings.
const (
	HeaderCaptchaToken = "X-Captcha-Token"
	HeaderAppPlatform  = "X-App-Platform"
)

// CaptchaConfig wires the dispatcher with per-platform verifiers. Callers
// build this once at startup and pass into Captcha().
type CaptchaConfig struct {
	Verifier captcha.Verifier
	// Action is an optional binding string passed to the verifier. Useful
	// for Turnstile actions (token scoped to "login" vs "register") and
	// for binding the App Attest assertion to a particular surface.
	Action string
}

// Captcha returns a Fiber middleware that enforces a passing captcha
// verification before the handler runs.
//
// The verifier comes from the package-level CaptchaConfig wired by main.go.
// If verifier is nil → middleware logs once and acts as a no-op (defense
// against forgetting to wire it).
func Captcha(cfg CaptchaConfig) fiber.Handler {
	verifier := cfg.Verifier
	if verifier == nil {
		log.Println("[captcha] WARNING: middleware mounted without a verifier; running as no-op")
		verifier = captcha.NewNoop("middleware: no verifier wired")
	}
	return func(c *fiber.Ctx) error {
		token := c.Get(HeaderCaptchaToken)
		if token == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "captcha token required",
				"code":  "CAPTCHA_MISSING",
			})
		}

		platform := captcha.Platform(c.Get(HeaderAppPlatform))
		if platform == "" {
			platform = captcha.PlatformWeb
		}
		switch platform {
		case captcha.PlatformWeb, captcha.PlatformIOS, captcha.PlatformAndroid:
			// ok
		default:
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "unknown platform",
				"code":  "CAPTCHA_PLATFORM_UNKNOWN",
			})
		}

		err := verifier.Verify(
			c.Context(),
			token,
			c.IP(),
			captcha.EncodePlatformAction(platform, cfg.Action),
		)
		if err != nil {
			// SECURITY: log the full error so operators can debug
			// rejections (Cloudflare error codes, App Attest counter
			// regressions, etc.), but only return a generic code to the
			// caller. Leaking the reason would help legitimate bots
			// adapt.
			rid, _ := c.Locals("requestid").(string)
			log.Printf("[captcha] rid=%s platform=%s reject: %v", rid, platform, err)
			if errors.Is(err, captcha.ErrMissingToken) {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"error": "captcha token required",
					"code":  "CAPTCHA_MISSING",
				})
			}
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{
				"error": "captcha_failed",
				"code":  "BOT_DETECTED",
			})
		}

		c.Locals("captcha_verified", true)
		return c.Next()
	}
}
