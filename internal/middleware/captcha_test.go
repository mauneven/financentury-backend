package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/the-financial-workspace/backend/internal/captcha"
)

// staticVerifier returns the provided error from every Verify call. It also
// records the action it received so tests can assert routing.
type staticVerifier struct {
	err        error
	lastToken  string
	lastIP     string
	lastAction string
	calls      int
}

func (s *staticVerifier) Verify(_ context.Context, token, ip, action string) error {
	s.calls++
	s.lastToken = token
	s.lastIP = ip
	s.lastAction = action
	return s.err
}

// readBody is a tiny helper that drains + closes the response body and
// returns the bytes. Keeps tests readable.
func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// TestCaptchaMiddleware_NoTokenReturns400 verifies the missing-token path.
func TestCaptchaMiddleware_NoTokenReturns400(t *testing.T) {
	app := fiber.New()
	app.Post("/x", Captcha(CaptchaConfig{Verifier: &staticVerifier{}}), func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(string(body), "CAPTCHA_MISSING") {
		t.Errorf("body = %s, want CAPTCHA_MISSING code", body)
	}
}

// TestCaptchaMiddleware_HappyPathContinues verifies that a successful
// verification sets the locals + calls the handler.
func TestCaptchaMiddleware_HappyPathContinues(t *testing.T) {
	v := &staticVerifier{}
	var verifiedLocal bool
	app := fiber.New()
	app.Post("/x",
		Captcha(CaptchaConfig{Verifier: v, Action: "login"}),
		func(c *fiber.Ctx) error {
			val, _ := c.Locals("captcha_verified").(bool)
			verifiedLocal = val
			return c.JSON(fiber.Map{"ok": true})
		})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set(HeaderCaptchaToken, "tok-1")
	req.Header.Set(HeaderAppPlatform, "web")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	_ = readBody(t, resp)

	if v.calls != 1 {
		t.Errorf("verifier calls = %d, want 1", v.calls)
	}
	if v.lastToken != "tok-1" {
		t.Errorf("lastToken = %q", v.lastToken)
	}
	// Action is packed as "platform|action" by EncodePlatformAction.
	if !strings.HasSuffix(v.lastAction, "|login") {
		t.Errorf("lastAction = %q, want suffix |login", v.lastAction)
	}
	if !strings.HasPrefix(v.lastAction, "web|") {
		t.Errorf("lastAction = %q, want prefix web|", v.lastAction)
	}
	if !verifiedLocal {
		t.Error("c.Locals(captcha_verified) not set")
	}
}

// TestCaptchaMiddleware_BotDetectedReturns403 verifies the fail-closed
// path: a verifier rejection translates to 403 + BOT_DETECTED.
func TestCaptchaMiddleware_BotDetectedReturns403(t *testing.T) {
	v := &staticVerifier{err: captcha.ErrCaptchaFailed}
	app := fiber.New()
	app.Post("/x", Captcha(CaptchaConfig{Verifier: v}), func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set(HeaderCaptchaToken, "tok")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	body := readBody(t, resp)
	var decoded struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Code != "BOT_DETECTED" {
		t.Errorf("code = %q, want BOT_DETECTED", decoded.Code)
	}
	if decoded.Error != "captcha_failed" {
		t.Errorf("error = %q, want captcha_failed", decoded.Error)
	}
}

// TestCaptchaMiddleware_FailClosedOnNonSentinelError verifies that any
// non-ErrCaptchaFailed error still results in 403. A Cloudflare outage
// is a captcha bypass risk — fail closed.
func TestCaptchaMiddleware_FailClosedOnNonSentinelError(t *testing.T) {
	v := &staticVerifier{err: errors.New("network broken")}
	app := fiber.New()
	app.Post("/x", Captcha(CaptchaConfig{Verifier: v}), func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set(HeaderCaptchaToken, "tok")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

// TestCaptchaMiddleware_MissingTokenSentinel verifies that
// captcha.ErrMissingToken from the verifier surfaces as 400, not 403 —
// the client just forgot the token, that's not a bot signal.
func TestCaptchaMiddleware_MissingTokenSentinel(t *testing.T) {
	v := &staticVerifier{err: captcha.ErrMissingToken}
	app := fiber.New()
	app.Post("/x", Captcha(CaptchaConfig{Verifier: v}), func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set(HeaderCaptchaToken, "tok") // present in header, verifier still says missing
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestCaptchaMiddleware_UnknownPlatform400 ensures an unsupported
// platform header is rejected before hitting the verifier.
func TestCaptchaMiddleware_UnknownPlatform400(t *testing.T) {
	v := &staticVerifier{}
	app := fiber.New()
	app.Post("/x", Captcha(CaptchaConfig{Verifier: v}), func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set(HeaderCaptchaToken, "t")
	req.Header.Set(HeaderAppPlatform, "windows-phone")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if v.calls != 0 {
		t.Errorf("verifier was called for unknown platform — must short-circuit at middleware")
	}
}

// TestCaptchaMiddleware_NoVerifierActsAsNoop verifies the defensive
// behaviour when a middleware is mounted without wiring a verifier — it
// must NOT crash, must NOT block, and must log a warning. Production
// wiring goes through main.go which always sets a verifier (possibly
// no-op), so this is purely a regression guard.
func TestCaptchaMiddleware_NoVerifierActsAsNoop(t *testing.T) {
	app := fiber.New()
	app.Post("/x", Captcha(CaptchaConfig{}), func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set(HeaderCaptchaToken, "anything")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestCaptchaMiddleware_DefaultPlatformIsWeb verifies the platform
// inference: when X-App-Platform is missing, the verifier action prefix
// must be "web|".
func TestCaptchaMiddleware_DefaultPlatformIsWeb(t *testing.T) {
	v := &staticVerifier{}
	app := fiber.New()
	app.Post("/x", Captcha(CaptchaConfig{Verifier: v, Action: "any"}), func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set(HeaderCaptchaToken, "t")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = readBody(t, resp)
	if !strings.HasPrefix(v.lastAction, "web|") {
		t.Errorf("lastAction = %q, want web| prefix", v.lastAction)
	}
}
