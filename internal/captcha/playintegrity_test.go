package captcha

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// buildPlayIntegrityToken constructs a synthetic Play Integrity token: a
// JWE wrapping a JWS, with both layers using the algorithms the verifier
// expects (A256KW + A256GCM, ES256). The result can be fed to
// playIntegrityVerifier.Verify just like a real Play Integrity token.
func buildPlayIntegrityToken(t *testing.T, kek []byte, signer *ecdsa.PrivateKey, claims playIntegrityClaims) string {
	t.Helper()

	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	// JWS
	header := `{"alg":"ES256","typ":"JWT"}`
	h := base64.RawURLEncoding.EncodeToString([]byte(header))
	p := base64.RawURLEncoding.EncodeToString(payload)
	signed := h + "." + p
	digest := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, signer, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig := make([]byte, 64)
	rb := r.Bytes()
	sb := s.Bytes()
	copy(sig[32-len(rb):32], rb)
	copy(sig[64-len(sb):64], sb)
	jws := signed + "." + base64.RawURLEncoding.EncodeToString(sig)

	// JWE — A256KW + A256GCM
	jweHeader := `{"alg":"A256KW","enc":"A256GCM"}`
	jweHeaderB64 := base64.RawURLEncoding.EncodeToString([]byte(jweHeader))
	cek := make([]byte, 32)
	if _, err := rand.Read(cek); err != nil {
		t.Fatalf("rand cek: %v", err)
	}
	wrapped, err := aesKeyWrap(kek, cek)
	if err != nil {
		t.Fatalf("aesKeyWrap: %v", err)
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		t.Fatalf("rand iv: %v", err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("gcm: %v", err)
	}
	aad := []byte(jweHeaderB64)
	sealed := gcm.Seal(nil, iv, []byte(jws), aad)
	ct := sealed[:len(sealed)-16]
	tag := sealed[len(sealed)-16:]

	return strings.Join([]string{
		jweHeaderB64,
		base64.RawURLEncoding.EncodeToString(wrapped),
		base64.RawURLEncoding.EncodeToString(iv),
		base64.RawURLEncoding.EncodeToString(ct),
		base64.RawURLEncoding.EncodeToString(tag),
	}, ".")
}

// aesKeyWrap is the inverse of aesKeyUnwrap, used only by tests.
func aesKeyWrap(kek, key []byte) ([]byte, error) {
	if len(key)%8 != 0 {
		return nil, errors.New("key length not multiple of 8")
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	const iv = uint64(0xA6A6A6A6A6A6A6A6)
	n := len(key) / 8
	A := make([]byte, 8)
	for i := 0; i < 8; i++ {
		A[i] = byte(iv >> (56 - 8*i))
	}
	R := make([][]byte, n)
	for i := 0; i < n; i++ {
		R[i] = make([]byte, 8)
		copy(R[i], key[8*i:8*(i+1)])
	}
	buf := make([]byte, 16)
	for j := 0; j < 6; j++ {
		for i := 1; i <= n; i++ {
			copy(buf[:8], A)
			copy(buf[8:], R[i-1])
			block.Encrypt(buf, buf)
			copy(A, buf[:8])
			t := uint64(n*j + i)
			A[0] ^= byte(t >> 56)
			A[1] ^= byte(t >> 48)
			A[2] ^= byte(t >> 40)
			A[3] ^= byte(t >> 32)
			A[4] ^= byte(t >> 24)
			A[5] ^= byte(t >> 16)
			A[6] ^= byte(t >> 8)
			A[7] ^= byte(t)
			copy(R[i-1], buf[8:])
		}
	}
	out := make([]byte, 0, 8+n*8)
	out = append(out, A...)
	for _, r := range R {
		out = append(out, r...)
	}
	return out, nil
}

// TestPlayIntegrity_HappyPath wires a fresh JWE + JWS pair and verifies
// the resulting token passes Verify.
func TestPlayIntegrity_HappyPath(t *testing.T) {
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}

	verB64 := base64.StdEncoding.EncodeToString(uncompressedPubKey(t, &priv.PublicKey))
	kekB64 := base64.StdEncoding.EncodeToString(kek)
	cs := NewInMemoryChallengeStore()
	nonce := []byte("nonce-12345")
	cs.Issue(nonce)
	v := NewGooglePlayIntegrity("com.example.app", kekB64, verB64, cs)

	var claims playIntegrityClaims
	claims.RequestDetails.RequestPackageName = "com.example.app"
	claims.RequestDetails.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
	claims.AppIntegrity.AppRecognitionVerdict = "PLAY_RECOGNIZED"
	claims.AppIntegrity.PackageName = "com.example.app"

	token := buildPlayIntegrityToken(t, kek, priv, claims)
	if err := v.Verify(context.Background(), token, "", ""); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}
}

// TestPlayIntegrity_FailureModes covers the documented rejection paths.
func TestPlayIntegrity_FailureModes(t *testing.T) {
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	priv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	verB64 := base64.StdEncoding.EncodeToString(uncompressedPubKey(t, &priv.PublicKey))
	kekB64 := base64.StdEncoding.EncodeToString(kek)

	t.Run("package_mismatch", func(t *testing.T) {
		cs := NewInMemoryChallengeStore()
		nonce := []byte("n1")
		cs.Issue(nonce)
		v := NewGooglePlayIntegrity("com.expected", kekB64, verB64, cs)
		var claims playIntegrityClaims
		claims.RequestDetails.RequestPackageName = "com.OTHER"
		claims.RequestDetails.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
		token := buildPlayIntegrityToken(t, kek, priv, claims)
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("Verify() = %v, want ErrCaptchaFailed", err)
		}
	})

	t.Run("app_verdict_unrecognized", func(t *testing.T) {
		cs := NewInMemoryChallengeStore()
		nonce := []byte("n2")
		cs.Issue(nonce)
		v := NewGooglePlayIntegrity("com.example.app", kekB64, verB64, cs)
		var claims playIntegrityClaims
		claims.RequestDetails.RequestPackageName = "com.example.app"
		claims.RequestDetails.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
		claims.AppIntegrity.AppRecognitionVerdict = "UNRECOGNIZED_VERSION"
		token := buildPlayIntegrityToken(t, kek, priv, claims)
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("Verify() = %v, want ErrCaptchaFailed", err)
		}
	})

	t.Run("nonce_unknown", func(t *testing.T) {
		cs := NewInMemoryChallengeStore()
		v := NewGooglePlayIntegrity("com.example.app", kekB64, verB64, cs)
		var claims playIntegrityClaims
		claims.RequestDetails.RequestPackageName = "com.example.app"
		claims.RequestDetails.Nonce = base64.RawURLEncoding.EncodeToString([]byte("never-issued"))
		token := buildPlayIntegrityToken(t, kek, priv, claims)
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrChallengeInvalid) {
			t.Fatalf("Verify() = %v, want ErrChallengeInvalid", err)
		}
	})

	t.Run("wrong_signer", func(t *testing.T) {
		cs := NewInMemoryChallengeStore()
		nonce := []byte("n3")
		cs.Issue(nonce)
		v := NewGooglePlayIntegrity("com.example.app", kekB64, verB64, cs)
		// Sign with a different key.
		otherPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		var claims playIntegrityClaims
		claims.RequestDetails.RequestPackageName = "com.example.app"
		claims.RequestDetails.Nonce = base64.RawURLEncoding.EncodeToString(nonce)
		token := buildPlayIntegrityToken(t, kek, otherPriv, claims)
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("Verify() = %v, want ErrCaptchaFailed", err)
		}
	})

	t.Run("missing_token", func(t *testing.T) {
		cs := NewInMemoryChallengeStore()
		v := NewGooglePlayIntegrity("com.example.app", kekB64, verB64, cs)
		err := v.Verify(context.Background(), "", "", "")
		if !errors.Is(err, ErrMissingToken) {
			t.Fatalf("Verify() = %v, want ErrMissingToken", err)
		}
	})
}

// TestNewPlayIntegrity_NoopOnEmpty verifies fail-open dev path.
func TestNewPlayIntegrity_NoopOnEmpty(t *testing.T) {
	for _, tc := range []struct{ pkg, dec, ver string }{
		{"", "ZGVj", "dmVy"},
		{"com.x", "", "dmVy"},
		{"com.x", "ZGVj", ""},
	} {
		v := NewGooglePlayIntegrity(tc.pkg, tc.dec, tc.ver, nil)
		if Reason(v) == "" {
			t.Errorf("expected noop for (pkg=%q dec=%q ver=%q)", tc.pkg, tc.dec, tc.ver)
		}
		if err := v.Verify(context.Background(), "x", "", ""); err != nil {
			t.Errorf("noop Verify() = %v, want nil", err)
		}
	}
}

// TestNewPlayIntegrity_RejectMisconfigured verifies that present-but-broken
// secrets surface as a rejecting verifier (not a noop) — the operator
// should notice the misconfiguration on first request.
func TestNewPlayIntegrity_RejectMisconfigured(t *testing.T) {
	// Decryption key too short.
	v := NewGooglePlayIntegrity("com.x", base64.StdEncoding.EncodeToString([]byte("short")), base64.StdEncoding.EncodeToString([]byte("alsoshort")), nil)
	err := v.Verify(context.Background(), "x", "", "")
	if !errors.Is(err, ErrCaptchaFailed) {
		t.Fatalf("Verify() = %v, want ErrCaptchaFailed for misconfigured verifier", err)
	}
}

// TestAesKeyWrapUnwrap round-trips a 32-byte CEK through our key wrap
// helpers — guards against silent regressions in the AES-KW
// implementation.
func TestAesKeyWrapUnwrap(t *testing.T) {
	kek := make([]byte, 32)
	_, _ = rand.Read(kek)
	cek := make([]byte, 32)
	_, _ = rand.Read(cek)
	wrapped, err := aesKeyWrap(kek, cek)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	got, err := aesKeyUnwrap(kek, wrapped)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if string(got) != string(cek) {
		t.Fatal("round-tripped CEK differs")
	}
}

// uncompressedPubKey emits a DER-encoded PKIX public key — the format
// parseECDSAVerificationKey accepts. We chose this over the raw 65-byte
// 0x04|x|y representation specifically to avoid touching deprecated
// big.Int coordinates on ecdsa.PublicKey (Go 1.26 SA1019).
func uncompressedPubKey(t *testing.T, pub *ecdsa.PublicKey) []byte {
	t.Helper()
	der, err := MarshalECDSAPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	return der
}
