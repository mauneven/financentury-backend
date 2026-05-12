package captcha

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
)

// generateP256Key returns a freshly-minted P-256 ECDSA private key plus its
// public key. Used as the simulated App Attest registered key.
func generateP256Key(t *testing.T) (*ecdsa.PrivateKey, *ecdsa.PublicKey) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv, &priv.PublicKey
}

// buildAuthData constructs an authData blob matching Apple's format:
// rpIdHash(32) | flags(1) | signCount(4). The keyID we wire below pulls
// the rpIdHash from the verifier directly so we can skip that match if
// desired.
func buildAuthData(rpIDHash []byte, counter uint32) []byte {
	buf := make([]byte, 37)
	copy(buf[:32], rpIDHash)
	buf[32] = 0x01 // flags
	binary.BigEndian.PutUint32(buf[33:37], counter)
	return buf
}

// signAssertion builds a CBOR-encoded App Attest assertion with the given
// authData + a fresh ECDSA signature over sha256(authData || clientDataHash).
func signAssertion(t *testing.T, priv *ecdsa.PrivateKey, authData, clientDataHash []byte) []byte {
	t.Helper()
	hasher := sha256.New()
	hasher.Write(authData)
	hasher.Write(clientDataHash)
	nonce := hasher.Sum(nil)
	sig, err := ecdsa.SignASN1(rand.Reader, priv, nonce)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return encodeAssertion(authData, sig)
}

// buildToken packages an assertion + the surrounding envelope, then
// base64-encodes the whole thing the way the iOS client would.
func buildToken(t *testing.T, keyID string, assertion, clientDataHash, challenge []byte) string {
	t.Helper()
	env, err := json.Marshal(appleAssertion{
		KeyID:          keyID,
		Assertion:      assertion,
		ClientDataHash: clientDataHash,
		Challenge:      challenge,
	})
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	return base64.StdEncoding.EncodeToString(env)
}

// TestAppAttest_HappyPath signs a fresh assertion with a registered key,
// posts it through the verifier, and asserts success + counter advancement.
func TestAppAttest_HappyPath(t *testing.T) {
	priv, pub := generateP256Key(t)
	keyID := "test-key-1"

	ks := NewInMemoryKeyStore()
	ks.Register(keyID, pub, 0)
	cs := NewInMemoryChallengeStore()

	v := NewAppleAppAttest("ABC1234567", "com.example.app", ks, cs).(*appAttestVerifier)
	authData := buildAuthData(v.rpIDHash, 1)
	clientData := sha256.Sum256([]byte("request-body-canonical"))
	challenge := []byte("challenge-1234567890")
	cs.Issue(challenge)

	assertion := signAssertion(t, priv, authData, clientData[:])
	token := buildToken(t, keyID, assertion, clientData[:], challenge)

	if err := v.Verify(context.Background(), token, "", ""); err != nil {
		t.Fatalf("Verify() = %v, want nil", err)
	}

	// Counter must have advanced to 1.
	_, counter, err := ks.Lookup(context.Background(), keyID)
	if err != nil {
		t.Fatalf("Lookup() = %v", err)
	}
	if counter != 1 {
		t.Errorf("counter = %d, want 1", counter)
	}
}

// TestAppAttest_FailureModes table-tests every documented failure path.
func TestAppAttest_FailureModes(t *testing.T) {
	priv, pub := generateP256Key(t)
	keyID := "test-key-2"

	makeVerifier := func() (*appAttestVerifier, *InMemoryKeyStore, *InMemoryChallengeStore) {
		ks := NewInMemoryKeyStore()
		ks.Register(keyID, pub, 5) // last counter = 5
		cs := NewInMemoryChallengeStore()
		v := NewAppleAppAttest("TEAM", "com.example.app", ks, cs).(*appAttestVerifier)
		return v, ks, cs
	}

	t.Run("missing_token", func(t *testing.T) {
		v, _, _ := makeVerifier()
		err := v.Verify(context.Background(), "", "", "")
		if !errors.Is(err, ErrMissingToken) {
			t.Fatalf("Verify() = %v, want ErrMissingToken", err)
		}
	})

	t.Run("bad_base64", func(t *testing.T) {
		v, _, _ := makeVerifier()
		err := v.Verify(context.Background(), "!!not-b64!!", "", "")
		if err == nil {
			t.Fatal("Verify() = nil, want error")
		}
	})

	t.Run("missing_fields", func(t *testing.T) {
		v, _, cs := makeVerifier()
		cs.Issue([]byte("any"))
		token := base64.StdEncoding.EncodeToString([]byte(`{}`))
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("Verify() = %v, want ErrCaptchaFailed", err)
		}
	})

	t.Run("unknown_challenge", func(t *testing.T) {
		v, _, _ := makeVerifier()
		authData := buildAuthData(v.rpIDHash, 6)
		clientData := sha256.Sum256([]byte("body"))
		assertion := signAssertion(t, priv, authData, clientData[:])
		token := buildToken(t, keyID, assertion, clientData[:], []byte("never-issued"))
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrChallengeInvalid) {
			t.Fatalf("Verify() = %v, want ErrChallengeInvalid", err)
		}
	})

	t.Run("unknown_keyID", func(t *testing.T) {
		v, _, cs := makeVerifier()
		challenge := []byte("ch1")
		cs.Issue(challenge)
		authData := buildAuthData(v.rpIDHash, 6)
		clientData := sha256.Sum256([]byte("body"))
		assertion := signAssertion(t, priv, authData, clientData[:])
		token := buildToken(t, "no-such-key", assertion, clientData[:], challenge)
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrKeyNotFound) {
			t.Fatalf("Verify() = %v, want ErrKeyNotFound", err)
		}
	})

	t.Run("rpID_mismatch", func(t *testing.T) {
		v, _, cs := makeVerifier()
		challenge := []byte("ch2")
		cs.Issue(challenge)
		// Wrong rpID — anything other than the verifier's hash.
		bogus := sha256.Sum256([]byte("DIFFERENT.bundle"))
		authData := buildAuthData(bogus[:], 6)
		clientData := sha256.Sum256([]byte("body"))
		assertion := signAssertion(t, priv, authData, clientData[:])
		token := buildToken(t, keyID, assertion, clientData[:], challenge)
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("Verify() = %v, want ErrCaptchaFailed", err)
		}
	})

	t.Run("counter_regression", func(t *testing.T) {
		v, _, cs := makeVerifier()
		challenge := []byte("ch3")
		cs.Issue(challenge)
		// Counter equal to last (5) — must reject (need strict >).
		authData := buildAuthData(v.rpIDHash, 5)
		clientData := sha256.Sum256([]byte("body"))
		assertion := signAssertion(t, priv, authData, clientData[:])
		token := buildToken(t, keyID, assertion, clientData[:], challenge)
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("Verify() = %v, want ErrCaptchaFailed", err)
		}
	})

	t.Run("signature_invalid", func(t *testing.T) {
		v, _, cs := makeVerifier()
		challenge := []byte("ch4")
		cs.Issue(challenge)
		// Sign with a different key.
		otherPriv, _ := generateP256Key(t)
		authData := buildAuthData(v.rpIDHash, 6)
		clientData := sha256.Sum256([]byte("body"))
		assertion := signAssertion(t, otherPriv, authData, clientData[:])
		token := buildToken(t, keyID, assertion, clientData[:], challenge)
		err := v.Verify(context.Background(), token, "", "")
		if !errors.Is(err, ErrCaptchaFailed) {
			t.Fatalf("Verify() = %v, want ErrCaptchaFailed", err)
		}
	})

	t.Run("replay_consumes_challenge", func(t *testing.T) {
		v, _, cs := makeVerifier()
		challenge := []byte("ch5")
		cs.Issue(challenge)
		authData := buildAuthData(v.rpIDHash, 6)
		clientData := sha256.Sum256([]byte("body"))
		assertion := signAssertion(t, priv, authData, clientData[:])
		token := buildToken(t, keyID, assertion, clientData[:], challenge)
		if err := v.Verify(context.Background(), token, "", ""); err != nil {
			t.Fatalf("first Verify() = %v", err)
		}
		// Re-presenting the same token must fail — challenge is consumed.
		if err := v.Verify(context.Background(), token, "", ""); !errors.Is(err, ErrChallengeInvalid) {
			t.Fatalf("replay Verify() = %v, want ErrChallengeInvalid", err)
		}
	})
}

// TestNewAppleAppAttest_NoopOnEmpty verifies the dev/staging path.
func TestNewAppleAppAttest_NoopOnEmpty(t *testing.T) {
	for _, tc := range []struct{ team, bundle string }{
		{"", "com.x"},
		{"TEAM", ""},
		{"", ""},
	} {
		v := NewAppleAppAttest(tc.team, tc.bundle, nil, nil)
		if Reason(v) == "" {
			t.Errorf("expected noop for (team=%q, bundle=%q)", tc.team, tc.bundle)
		}
		if err := v.Verify(context.Background(), "x", "", ""); err != nil {
			t.Errorf("noop Verify() = %v, want nil", err)
		}
	}
}

// TestAppAttest_KeyRoundTrip verifies that a key can be marshaled and
// parsed back without losing the curve / coordinates.
func TestAppAttest_KeyRoundTrip(t *testing.T) {
	_, pub := generateP256Key(t)
	der, err := MarshalECDSAPublicKey(pub)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := ParseECDSAPublicKey(der)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Compare the canonical PKIX DER encoding rather than the deprecated
	// big.Int coordinates. The round-trip is round-trip-stable.
	gotDER, err := MarshalECDSAPublicKey(got)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(gotDER) != string(der) {
		t.Fatal("round-tripped DER encoding differs")
	}
}

// TestEncodeDecodeAssertion exercises the tiny CBOR scanner via the
// matching encoder used in tests.
func TestEncodeDecodeAssertion(t *testing.T) {
	authData := []byte("auth-data-bytes")
	sig := []byte("signature-bytes")
	encoded := encodeAssertion(authData, sig)
	gotAuth, gotSig, err := decodeAssertion(encoded)
	if err != nil {
		t.Fatalf("decodeAssertion: %v", err)
	}
	if string(gotAuth) != string(authData) {
		t.Errorf("authData = %q, want %q", gotAuth, authData)
	}
	if string(gotSig) != string(sig) {
		t.Errorf("signature = %q, want %q", gotSig, sig)
	}
}
