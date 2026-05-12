package captcha

import (
	"context"
	"fmt"
	"strings"
)

// Platform is the value carried in the X-App-Platform header. The set is
// deliberately closed — unknown platforms fail-closed rather than fall back
// to a permissive default.
type Platform string

const (
	PlatformWeb     Platform = "web"
	PlatformIOS     Platform = "ios"
	PlatformAndroid Platform = "android"
)

// MultiConfig wires the three per-platform verifiers behind a single
// Verifier so the middleware doesn't carry platform-aware logic.
type MultiConfig struct {
	Web     Verifier
	IOS     Verifier
	Android Verifier
}

// NewMulti returns a Verifier that dispatches based on the platform string
// passed into Verify via the `action` parameter (overloaded for routing —
// see middleware/captcha.go which packs the platform there).
//
// We dispatch in Verify rather than at the middleware layer so a unit test
// of the dispatcher can be written without spinning up a Fiber app.
func NewMulti(cfg MultiConfig) Verifier {
	if cfg.Web == nil {
		cfg.Web = NewNoop("multi: web verifier unset")
	}
	if cfg.IOS == nil {
		cfg.IOS = NewNoop("multi: ios verifier unset")
	}
	if cfg.Android == nil {
		cfg.Android = NewNoop("multi: android verifier unset")
	}
	return multiVerifier{cfg: cfg}
}

type multiVerifier struct{ cfg MultiConfig }

// Verify routes by parsing the platform off the action prefix. The middleware
// packs "<platform>|<action>" so the Verifier can read both without
// changing the interface signature.
//
// Why overload action: the alternative was a separate VerifyPlatform method
// on the interface — but then every test would need to be updated when adding
// a new platform. Encoding the routing key into action keeps the interface
// stable and lets the middleware decide the routing convention.
func (m multiVerifier) Verify(ctx context.Context, token, remoteIP, action string) error {
	platform, realAction := splitPlatformAction(action)
	switch platform {
	case PlatformWeb, "":
		// Default to web if missing — matches the middleware behavior of
		// assuming web when X-App-Platform is empty.
		return m.cfg.Web.Verify(ctx, token, remoteIP, realAction)
	case PlatformIOS:
		return m.cfg.IOS.Verify(ctx, token, remoteIP, realAction)
	case PlatformAndroid:
		return m.cfg.Android.Verify(ctx, token, remoteIP, realAction)
	default:
		return fmt.Errorf("multi: unknown platform %q: %w", platform, ErrCaptchaFailed)
	}
}

// EncodePlatformAction packs a platform + action into a single string the
// multi-dispatcher can split on. Exposed so the middleware can use the same
// convention without duplicating the format.
func EncodePlatformAction(platform Platform, action string) string {
	return string(platform) + "|" + action
}

// splitPlatformAction is the inverse of EncodePlatformAction. Tolerant of
// inputs that don't carry the platform prefix (used by direct unit tests
// against the leaf verifiers).
func splitPlatformAction(s string) (Platform, string) {
	idx := strings.IndexByte(s, '|')
	if idx < 0 {
		return "", s
	}
	return Platform(s[:idx]), s[idx+1:]
}
