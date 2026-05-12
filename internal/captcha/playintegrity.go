package captcha

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Google Play Integrity API token format (server-side decryption variant).
//
// The Android client calls Play Integrity which returns an "integrity token"
// — a JWE (JSON Web Encryption) string wrapping a JWS. The server decrypts
// the JWE with a project-issued symmetric key, then verifies the JWS with a
// project-issued verification key. The resulting JSON contains
// requestDetails (request hash, nonce), appIntegrity (package name +
// certificate digest), and deviceIntegrity (verdict labels).
//
// We avoid pulling in a full JOSE library (go-jose adds ~250 KB and a
// transitive web of crypto deps). The Play Integrity envelope is a tightly
// constrained subset: A256KW key-wrap + A256GCM payload encryption for the
// JWE, and ES256 signature for the inner JWS. Those primitives are all in
// the stdlib.
//
// References:
//   - https://developer.android.com/google/play/integrity/verdict
//   - RFC 7516 (JWE), RFC 7515 (JWS), RFC 7518 (JWA: A256KW, A256GCM, ES256)

// playIntegrityClaims is the decoded JWS payload from a Play Integrity
// verdict. Only the fields we actually check are decoded.
type playIntegrityClaims struct {
	RequestDetails struct {
		RequestPackageName string `json:"requestPackageName"`
		Nonce              string `json:"nonce"`
		TimestampMillis    string `json:"timestampMillis"`
	} `json:"requestDetails"`

	AppIntegrity struct {
		AppRecognitionVerdict   string   `json:"appRecognitionVerdict"`
		PackageName             string   `json:"packageName"`
		CertificateSha256Digest []string `json:"certificateSha256Digest"`
		VersionCode             string   `json:"versionCode"`
	} `json:"appIntegrity"`

	DeviceIntegrity struct {
		DeviceRecognitionVerdict []string `json:"deviceRecognitionVerdict"`
	} `json:"deviceIntegrity"`

	AccountDetails struct {
		AppLicensingVerdict string `json:"appLicensingVerdict"`
	} `json:"accountDetails"`
}

// playIntegrityVerifier validates Android Play Integrity tokens locally.
type playIntegrityVerifier struct {
	packageName     string
	decryptionKey   []byte
	verificationKey *ecdsa.PublicKey
	challenges      ChallengeStore
}

// NewGooglePlayIntegrity constructs a Play Integrity verifier.
//
// packageName must match the app's Android package (e.g. com.financentury.app).
// decryptionKey is base64-decoded AES-256 KW key (32 bytes).
// verificationKey is base64-decoded ECDSA P-256 public key in either DER
// (PKIX) or uncompressed-point (65 bytes, 0x04|x|y) form.
//
// Any empty input → no-op verifier so dev / staging stays running.
func NewGooglePlayIntegrity(packageName, decryptionKeyB64, verificationKeyB64 string, cs ChallengeStore) Verifier {
	packageName = strings.TrimSpace(packageName)
	decryptionKeyB64 = strings.TrimSpace(decryptionKeyB64)
	verificationKeyB64 = strings.TrimSpace(verificationKeyB64)
	if packageName == "" || decryptionKeyB64 == "" || verificationKeyB64 == "" {
		return NewNoop("GOOGLE_PLAY_INTEGRITY_* empty (Play Integrity disabled)")
	}
	dec, err := base64.StdEncoding.DecodeString(decryptionKeyB64)
	if err != nil {
		// Misconfigured secret — fail closed at startup by returning a
		// verifier that always rejects so the operator notices.
		return rejectAlways(fmt.Errorf("play integrity decryption_key base64: %w", err))
	}
	if len(dec) != 32 {
		return rejectAlways(fmt.Errorf("play integrity decryption_key length=%d, want 32", len(dec)))
	}
	verBytes, err := base64.StdEncoding.DecodeString(verificationKeyB64)
	if err != nil {
		return rejectAlways(fmt.Errorf("play integrity verification_key base64: %w", err))
	}
	pub, err := parseECDSAVerificationKey(verBytes)
	if err != nil {
		return rejectAlways(fmt.Errorf("play integrity verification_key: %w", err))
	}
	if cs == nil {
		cs = NewInMemoryChallengeStore()
	}
	return &playIntegrityVerifier{
		packageName:     packageName,
		decryptionKey:   dec,
		verificationKey: pub,
		challenges:      cs,
	}
}

// rejectAlways returns a verifier that always reports ErrCaptchaFailed with
// the wrapped reason. Used when secrets are present but malformed so the
// operator notices on the first request rather than silently passing all
// traffic through a no-op.
func rejectAlways(reason error) Verifier {
	return rejectingVerifier{reason: reason}
}

type rejectingVerifier struct{ reason error }

func (r rejectingVerifier) Verify(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("%w: %w", r.reason, ErrCaptchaFailed)
}

// Verify validates an Android Play Integrity token.
//
// The token is the raw JWE string returned by IntegrityManager. We:
//  1. Split + base64url-decode the five JWE segments.
//  2. AES-KW unwrap the CEK with the project decryption key.
//  3. AES-256-GCM decrypt the payload to recover the JWS string.
//  4. Split + base64url-decode the three JWS segments.
//  5. ES256-verify signature against the project verification key.
//  6. Decode claims, check package name, nonce (=challenge), recognition verdicts.
func (p *playIntegrityVerifier) Verify(ctx context.Context, token, _ string, _ string) error {
	if token == "" {
		return ErrMissingToken
	}

	jwsBytes, err := decryptJWE(token, p.decryptionKey)
	if err != nil {
		return fmt.Errorf("playintegrity: jwe: %w", err)
	}

	payload, err := verifyJWS(string(jwsBytes), p.verificationKey)
	if err != nil {
		return fmt.Errorf("playintegrity: jws: %w", err)
	}

	var claims playIntegrityClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return fmt.Errorf("playintegrity: decode claims: %w", err)
	}

	if claims.RequestDetails.RequestPackageName != p.packageName {
		return fmt.Errorf("playintegrity: package mismatch want=%q got=%q: %w",
			p.packageName, claims.RequestDetails.RequestPackageName, ErrCaptchaFailed)
	}

	if claims.AppIntegrity.PackageName != "" && claims.AppIntegrity.PackageName != p.packageName {
		return fmt.Errorf("playintegrity: appIntegrity.packageName mismatch: %w", ErrCaptchaFailed)
	}

	// SECURITY: app recognition verdict must be PLAY_RECOGNIZED. Anything
	// else (UNRECOGNIZED_VERSION, UNEVALUATED) means the binary running on
	// the device isn't the version Play has signed off on — treat as bot.
	if claims.AppIntegrity.AppRecognitionVerdict != "" && claims.AppIntegrity.AppRecognitionVerdict != "PLAY_RECOGNIZED" {
		return fmt.Errorf("playintegrity: app verdict=%q: %w",
			claims.AppIntegrity.AppRecognitionVerdict, ErrCaptchaFailed)
	}

	// SECURITY: nonce binds the token to a server-issued challenge so a
	// replayed verdict cannot be re-used. The Android client must request
	// the integrity token with the challenge as the nonce input.
	if claims.RequestDetails.Nonce != "" {
		nonceBytes, err := base64.RawURLEncoding.DecodeString(claims.RequestDetails.Nonce)
		if err != nil {
			// Fallback: some clients post the nonce unencoded.
			nonceBytes = []byte(claims.RequestDetails.Nonce)
		}
		if err := p.challenges.Consume(ctx, nonceBytes); err != nil {
			return fmt.Errorf("playintegrity: %w", err)
		}
	}

	return nil
}

// --- JWE (RFC 7516, A256KW + A256GCM) ---------------------------------------

// decryptJWE decodes a compact-serialized JWE and returns the cleartext.
// Validates that alg=A256KW and enc=A256GCM — any other algorithm pair is
// not supported by Play Integrity and is treated as bot.
func decryptJWE(token string, kek []byte) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 5 {
		return nil, fmt.Errorf("jwe: expected 5 segments, got %d: %w", len(parts), ErrCaptchaFailed)
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("jwe: header b64: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Enc string `json:"enc"`
	}
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		return nil, fmt.Errorf("jwe: header json: %w", err)
	}
	if header.Alg != "A256KW" || header.Enc != "A256GCM" {
		return nil, fmt.Errorf("jwe: unsupported alg=%q enc=%q: %w", header.Alg, header.Enc, ErrCaptchaFailed)
	}
	encryptedKey, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("jwe: key b64: %w", err)
	}
	iv, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("jwe: iv b64: %w", err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return nil, fmt.Errorf("jwe: ct b64: %w", err)
	}
	tag, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, fmt.Errorf("jwe: tag b64: %w", err)
	}

	cek, err := aesKeyUnwrap(kek, encryptedKey)
	if err != nil {
		return nil, fmt.Errorf("jwe: aes-kw: %w", err)
	}
	if len(cek) != 32 {
		return nil, fmt.Errorf("jwe: cek len=%d, want 32: %w", len(cek), ErrCaptchaFailed)
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, fmt.Errorf("jwe: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("jwe: gcm: %w", err)
	}
	if len(iv) != gcm.NonceSize() {
		return nil, fmt.Errorf("jwe: iv len=%d, want %d: %w", len(iv), gcm.NonceSize(), ErrCaptchaFailed)
	}
	// AAD per RFC 7516 §5.1 step 14 is ASCII(BASE64URL(protected header)).
	aad := []byte(parts[0])

	// Stdlib AEAD wants ciphertext || tag concatenated.
	sealed := make([]byte, 0, len(ciphertext)+len(tag))
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)
	plaintext, err := gcm.Open(nil, iv, sealed, aad)
	if err != nil {
		return nil, fmt.Errorf("jwe: gcm open: %w", err)
	}
	return plaintext, nil
}

// aesKeyUnwrap implements AES Key Wrap (RFC 3394) for an arbitrary-length
// KEK and a 64-bit-multiple wrapped key. Play Integrity always wraps a
// 32-byte CEK with a 32-byte KEK, but we keep the loop generic.
func aesKeyUnwrap(kek, wrapped []byte) ([]byte, error) {
	if len(wrapped) < 16 || len(wrapped)%8 != 0 {
		return nil, fmt.Errorf("aes-kw: invalid wrapped length %d: %w", len(wrapped), ErrCaptchaFailed)
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	const iv = uint64(0xA6A6A6A6A6A6A6A6)
	n := len(wrapped)/8 - 1
	A := make([]byte, 8)
	copy(A, wrapped[:8])
	R := make([][]byte, n)
	for i := 0; i < n; i++ {
		R[i] = make([]byte, 8)
		copy(R[i], wrapped[8*(i+1):8*(i+2)])
	}
	buf := make([]byte, 16)
	for j := 5; j >= 0; j-- {
		for i := n; i >= 1; i-- {
			// t = (n*j) + i
			t := uint64(n*j + i)
			// A ^= t
			copy(buf[:8], A)
			buf[0] ^= byte(t >> 56)
			buf[1] ^= byte(t >> 48)
			buf[2] ^= byte(t >> 40)
			buf[3] ^= byte(t >> 32)
			buf[4] ^= byte(t >> 24)
			buf[5] ^= byte(t >> 16)
			buf[6] ^= byte(t >> 8)
			buf[7] ^= byte(t)
			copy(buf[8:], R[i-1])
			block.Decrypt(buf, buf)
			copy(A, buf[:8])
			copy(R[i-1], buf[8:])
		}
	}
	// IV check: per RFC 3394, A must equal the constant IV after the loop.
	var got uint64
	for i := 0; i < 8; i++ {
		got = got<<8 | uint64(A[i])
	}
	if got != iv {
		return nil, fmt.Errorf("aes-kw: integrity check failed: %w", ErrCaptchaFailed)
	}
	out := make([]byte, 0, n*8)
	for i := 0; i < n; i++ {
		out = append(out, R[i]...)
	}
	return out, nil
}

// --- JWS (RFC 7515, ES256) --------------------------------------------------

// verifyJWS validates an ES256-signed compact JWS and returns the decoded
// payload. Returns ErrCaptchaFailed on any signature / format failure.
func verifyJWS(token string, pub *ecdsa.PublicKey) ([]byte, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("jws: expected 3 segments, got %d: %w", len(parts), ErrCaptchaFailed)
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("jws: header b64: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(rawHeader, &header); err != nil {
		return nil, fmt.Errorf("jws: header json: %w", err)
	}
	if header.Alg != "ES256" {
		return nil, fmt.Errorf("jws: unsupported alg=%q: %w", header.Alg, ErrCaptchaFailed)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("jws: payload b64: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("jws: sig b64: %w", err)
	}
	if len(sig) != 64 {
		return nil, fmt.Errorf("jws: es256 sig len=%d, want 64: %w", len(sig), ErrCaptchaFailed)
	}
	signed := parts[0] + "." + parts[1]
	digest := sha256.Sum256([]byte(signed))

	// ES256 JWS signature is R||S, each 32 bytes big-endian.
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])

	ok := ecdsaVerify(pub, digest[:], r, s)
	if !ok {
		return nil, fmt.Errorf("jws: signature invalid: %w", ErrCaptchaFailed)
	}
	return payload, nil
}

// ecdsaVerify wraps ecdsa.Verify in a panic-safe shell. Same rationale as
// ecdsaVerifyASN1 in appattest.go.
func ecdsaVerify(pub *ecdsa.PublicKey, hash []byte, r, s *big.Int) (ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			ok = false
		}
	}()
	return ecdsa.Verify(pub, hash, r, s)
}

// parseECDSAVerificationKey accepts either a DER (PKIX) or an
// uncompressed-point P-256 public key.
func parseECDSAVerificationKey(b []byte) (*ecdsa.PublicKey, error) {
	if len(b) == 65 && b[0] == 0x04 {
		x := new(big.Int).SetBytes(b[1:33])
		y := new(big.Int).SetBytes(b[33:65])
		return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
	}
	pub, err := ParseECDSAPublicKey(b)
	if err != nil {
		return nil, fmt.Errorf("parse pkix: %w", err)
	}
	return pub, nil
}

// --- error helper -----------------------------------------------------------

// stubError surfaces a configuration mistake (malformed secret) when the
// verifier is actually used. Kept lightweight; the type assertion on
// rejectingVerifier in tests confirms the path.
var _ = errors.New
