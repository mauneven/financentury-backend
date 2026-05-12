package captcha

import (
	"context"
	"errors"
	"testing"
)

// recordingVerifier captures every Verify call so we can assert the
// multi-dispatcher routed correctly.
type recordingVerifier struct {
	calls  []string
	result error
}

func (r *recordingVerifier) Verify(_ context.Context, token, _ string, action string) error {
	r.calls = append(r.calls, token+"@"+action)
	return r.result
}

// TestMulti_DispatchesByPlatform verifies that EncodePlatformAction +
// NewMulti route to the right underlying verifier.
func TestMulti_DispatchesByPlatform(t *testing.T) {
	web := &recordingVerifier{}
	ios := &recordingVerifier{}
	android := &recordingVerifier{}
	m := NewMulti(MultiConfig{Web: web, IOS: ios, Android: android})

	cases := []struct {
		platform Platform
		want     *recordingVerifier
	}{
		{PlatformWeb, web},
		{PlatformIOS, ios},
		{PlatformAndroid, android},
	}
	for _, tc := range cases {
		t.Run(string(tc.platform), func(t *testing.T) {
			err := m.Verify(context.Background(), "tok", "", EncodePlatformAction(tc.platform, "login"))
			if err != nil {
				t.Fatalf("Verify() = %v, want nil", err)
			}
			if len(tc.want.calls) != 1 {
				t.Fatalf("%s verifier received %d calls, want 1", tc.platform, len(tc.want.calls))
			}
			tc.want.calls = nil // reset
		})
	}
}

// TestMulti_EmptyPlatformRoutesToWeb mirrors the middleware behaviour
// where X-App-Platform absent → "web".
func TestMulti_EmptyPlatformRoutesToWeb(t *testing.T) {
	web := &recordingVerifier{}
	ios := &recordingVerifier{}
	m := NewMulti(MultiConfig{Web: web, IOS: ios})
	err := m.Verify(context.Background(), "tok", "", "|action")
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if len(web.calls) != 1 {
		t.Fatalf("web received %d, want 1", len(web.calls))
	}
	if len(ios.calls) != 0 {
		t.Fatalf("ios received %d, want 0", len(ios.calls))
	}
}

// TestMulti_UnknownPlatformFailsClosed verifies that an unrecognized
// platform routes to a rejection rather than silently passing.
func TestMulti_UnknownPlatformFailsClosed(t *testing.T) {
	m := NewMulti(MultiConfig{})
	err := m.Verify(context.Background(), "tok", "", "windows-phone|login")
	if !errors.Is(err, ErrCaptchaFailed) {
		t.Fatalf("Verify() = %v, want ErrCaptchaFailed", err)
	}
}

// TestMulti_NilVerifiersDefaultToNoop guards against a misconfigured
// dispatcher panicking on a nil leaf verifier.
func TestMulti_NilVerifiersDefaultToNoop(t *testing.T) {
	m := NewMulti(MultiConfig{}) // all nil
	if err := m.Verify(context.Background(), "tok", "", EncodePlatformAction(PlatformWeb, "x")); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if err := m.Verify(context.Background(), "tok", "", EncodePlatformAction(PlatformIOS, "x")); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
	if err := m.Verify(context.Background(), "tok", "", EncodePlatformAction(PlatformAndroid, "x")); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

// TestEncodeSplit round-trips the platform+action packing.
func TestEncodeSplit(t *testing.T) {
	p, a := splitPlatformAction(EncodePlatformAction(PlatformIOS, "register"))
	if p != PlatformIOS {
		t.Errorf("platform = %q, want %q", p, PlatformIOS)
	}
	if a != "register" {
		t.Errorf("action = %q, want %q", a, "register")
	}
}
