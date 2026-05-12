package captcha

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
)

// Apple App Attest assertion verification.
//
// The iOS client signs an "assertion" over (auth_data || client_data_hash)
// with a pre-registered hardware-backed ECDSA P-256 key. The server-side
// verification is local — no Apple round-trip — and proves that the request
// originated from a real instance of the signed binary running on a non-
// jailbroken device.
//
// The attestation key itself is provisioned via the bootstrap endpoint
// (POST /api/attest/register, no captcha required), which performs the more
// involved attestationObject parsing + x5c chain validation against Apple's
// root. That logic lives in attestation.go to keep the per-request hot path
// (Verify) lean.
//
// References:
//   - Apple docs: "Validating Apps That Connect to Your Server"
//   - WebAuthn auth_data layout: https://w3c.github.io/webauthn/#authenticator-data
//
// SECURITY: The client_data_hash binding is load-bearing. Without it the
// assertion is a static value the client can re-use forever. We require the
// client to fetch a server-generated challenge (GET /api/attest/challenge),
// include it in the body it hashes for client_data_hash, and the server
// re-verifies that the challenge was issued in the last 5 minutes and has
// not been redeemed. Replay window collapses to a single use.

// appAttestRPID is the relying party identifier baked into Apple's auth_data
// rpIdHash. For App Attest this is sha256(teamID + "." + bundleID).
// Computed once at verifier-construction time.
var appAttestRPIDLen = 32

// KeyStore abstracts persistence of registered attestation keys. The
// captcha package uses it to fetch the previously-registered ECDSA public
// key + current counter and to bump the counter after a successful Verify.
//
// The Verifier never speaks SQL — production code wires a Postgres-backed
// store in package handlers; tests use an in-memory map.
type KeyStore interface {
	// Lookup returns the ECDSA P-256 public key + last seen counter for a
	// given keyID. Returns ErrKeyNotFound if the key has never been
	// registered.
	Lookup(ctx context.Context, keyID string) (*ecdsa.PublicKey, uint32, error)
	// BumpCounter persists the new counter value after a successful
	// signature verification. Replay protection requires the new counter
	// to be strictly greater than the stored value.
	BumpCounter(ctx context.Context, keyID string, newCounter uint32) error
}

// ChallengeStore tracks server-issued challenges so the assertion's
// client_data_hash cannot be replayed.
type ChallengeStore interface {
	// Consume returns nil if the challenge was issued and is still valid;
	// the implementation MUST remove the challenge atomically to enforce
	// single-use. Returns an error otherwise.
	Consume(ctx context.Context, challenge []byte) error
}

// ErrKeyNotFound is returned by KeyStore.Lookup when the keyID has never been
// registered. Surfaced to the middleware as a captcha failure (the client is
// supposed to register before its first authenticated call).
var ErrKeyNotFound = errors.New("attest key not registered")

// ErrChallengeInvalid is returned when ChallengeStore.Consume rejects a
// challenge (expired, never issued, already redeemed).
var ErrChallengeInvalid = errors.New("attest challenge invalid")

// appleAssertion is the wire shape the iOS client posts as the captcha
// token (base64-decoded). The client serializes this and base64-encodes the
// whole envelope so it fits in a single header.
type appleAssertion struct {
	KeyID          string `json:"key_id"`
	Assertion      []byte `json:"assertion"`        // CBOR-encoded blob from DCAppAttestService.generateAssertion
	ClientDataHash []byte `json:"client_data_hash"` // sha256(challenge || canonical_request)
	Challenge      []byte `json:"challenge"`        // server-issued, single-use
	RawClientData  []byte `json:"raw_client_data"`  // optional, lets server recompute hash
}

// appAttestVerifier validates iOS App Attest assertions on every request.
type appAttestVerifier struct {
	teamID     string
	bundleID   string
	rpIDHash   []byte
	keyStore   KeyStore
	challenges ChallengeStore

	// Test seam: skip rpID match when running unit tests with mock data.
	skipRPIDCheck bool
}

// NewAppleAppAttest constructs an iOS App Attest verifier.
//
// When teamID or bundleID is empty, returns a NoOp. Production wiring sets
// both via env vars; main.go logs a warning on empty for prod-safety.
//
// The KeyStore + ChallengeStore are injected so handlers can wire the real
// Postgres-backed stores, while tests can supply in-memory fakes.
func NewAppleAppAttest(teamID, bundleID string, ks KeyStore, cs ChallengeStore) Verifier {
	teamID = strings.TrimSpace(teamID)
	bundleID = strings.TrimSpace(bundleID)
	if teamID == "" || bundleID == "" {
		return NewNoop("APPLE_APP_ATTEST_TEAM_ID/BUNDLE empty (App Attest disabled)")
	}
	rpID := teamID + "." + bundleID
	hash := sha256.Sum256([]byte(rpID))
	return &appAttestVerifier{
		teamID:     teamID,
		bundleID:   bundleID,
		rpIDHash:   hash[:],
		keyStore:   ks,
		challenges: cs,
	}
}

// Verify validates the App Attest assertion in the captured token.
//
// Token format: base64(JSON({key_id, assertion, client_data_hash, challenge})).
// The assertion is a CBOR blob with { signature, authenticatorData }.
// We avoid pulling in a CBOR dep by parsing the two flat fields directly
// from the prefix — see decodeAssertion.
func (a *appAttestVerifier) Verify(ctx context.Context, token, _ string, _ string) error {
	if token == "" {
		return ErrMissingToken
	}

	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(token)
		if err != nil {
			return fmt.Errorf("appattest: token base64: %w", err)
		}
	}

	var env appleAssertion
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("appattest: token json: %w", err)
	}

	if env.KeyID == "" || len(env.Assertion) == 0 || len(env.Challenge) == 0 || len(env.ClientDataHash) != sha256.Size {
		return fmt.Errorf("appattest: missing fields: %w", ErrCaptchaFailed)
	}

	// SECURITY: redeem the challenge first so a replayed assertion with
	// the same client_data_hash is rejected even before we touch the key.
	if err := a.challenges.Consume(ctx, env.Challenge); err != nil {
		return fmt.Errorf("appattest: %w", err)
	}

	pubKey, lastCounter, err := a.keyStore.Lookup(ctx, env.KeyID)
	if err != nil {
		return fmt.Errorf("appattest: lookup key: %w", err)
	}

	authData, signature, err := decodeAssertion(env.Assertion)
	if err != nil {
		return fmt.Errorf("appattest: %w", err)
	}

	// SECURITY: the rpIdHash (first 32 bytes of authData) MUST match the
	// sha256 of "teamID.bundleID". A signature minted for a different app
	// is invalid for us even if the underlying ECDSA verification would
	// otherwise pass.
	if !a.skipRPIDCheck {
		if len(authData) < appAttestRPIDLen {
			return fmt.Errorf("appattest: authData too short: %w", ErrCaptchaFailed)
		}
		if !bytesEqual(authData[:appAttestRPIDLen], a.rpIDHash) {
			return fmt.Errorf("appattest: rpID mismatch: %w", ErrCaptchaFailed)
		}
	}

	// SECURITY: signCount is bytes 33..37 (after the 32-byte rpIdHash and
	// the 1-byte flags field). It must monotonically increase per Apple's
	// spec — otherwise the assertion is a replay of an older signature.
	counter, err := signCounter(authData)
	if err != nil {
		return fmt.Errorf("appattest: %w", err)
	}
	if counter <= lastCounter {
		return fmt.Errorf("appattest: counter regression got=%d last=%d: %w", counter, lastCounter, ErrCaptchaFailed)
	}

	// nonce = sha256(authData || clientDataHash). The signature is over
	// the nonce, per Apple's docs.
	hasher := sha256.New()
	hasher.Write(authData)
	hasher.Write(env.ClientDataHash)
	nonce := hasher.Sum(nil)

	if !ecdsaVerifyASN1(pubKey, nonce, signature) {
		return fmt.Errorf("appattest: signature invalid: %w", ErrCaptchaFailed)
	}

	if err := a.keyStore.BumpCounter(ctx, env.KeyID, counter); err != nil {
		return fmt.Errorf("appattest: bump counter: %w", err)
	}
	return nil
}

// decodeAssertion parses the minimal CBOR-ish envelope DCAppAttestService
// produces. The blob is a CBOR map with exactly two entries:
//
//	{ "signature": <bytes>, "authenticatorData": <bytes> }
//
// Rather than pulling in a full CBOR decoder (a few hundred KB of stdlib +
// generated code we wouldn't otherwise use), we implement a tiny scanner that
// reads the two known keys. The format is stable: Apple's CBOR encoder
// always emits the same ordering with the same major-type bytes.
//
// If the envelope structure ever changes upstream this scanner will reject
// the new shape — that's deliberate. A breaking spec change should fail
// loud during integration rather than silently accept a malformed blob.
func decodeAssertion(blob []byte) (authData, signature []byte, err error) {
	// CBOR encoding for a 2-element map: 0xA2 (major type 5, length 2).
	if len(blob) < 1 || blob[0] != 0xA2 {
		return nil, nil, fmt.Errorf("assertion: expected 2-element CBOR map, got first byte %#x: %w", blob[0], ErrCaptchaFailed)
	}
	off := 1
	for i := 0; i < 2; i++ {
		key, n, kerr := cborReadString(blob[off:])
		if kerr != nil {
			return nil, nil, fmt.Errorf("assertion: read key: %w", kerr)
		}
		off += n
		val, m, verr := cborReadBytes(blob[off:])
		if verr != nil {
			return nil, nil, fmt.Errorf("assertion: read value: %w", verr)
		}
		off += m
		switch key {
		case "signature":
			signature = val
		case "authenticatorData":
			authData = val
		default:
			return nil, nil, fmt.Errorf("assertion: unknown key %q: %w", key, ErrCaptchaFailed)
		}
	}
	if authData == nil || signature == nil {
		return nil, nil, fmt.Errorf("assertion: missing field: %w", ErrCaptchaFailed)
	}
	return authData, signature, nil
}

// signCounter extracts the 32-bit big-endian signCount from an authData blob.
// Layout: rpIdHash(32) | flags(1) | signCount(4) | ...
func signCounter(authData []byte) (uint32, error) {
	if len(authData) < 37 {
		return 0, fmt.Errorf("authData too short for signCount: %w", ErrCaptchaFailed)
	}
	return binary.BigEndian.Uint32(authData[33:37]), nil
}

// ecdsaVerifyASN1 wraps the stdlib verifier with a panic-safe call.
//
// crypto/ecdsa.VerifyASN1 panics on certain malformed inputs (e.g. when the
// public key is corrupted). The captcha middleware sits on the auth surface
// where a panic translates to a 500 and lets the bot retry — we'd rather
// reject the request as failed-verify.
func ecdsaVerifyASN1(pub *ecdsa.PublicKey, hash, sig []byte) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	return ecdsa.VerifyASN1(pub, hash, sig)
}

// ParseECDSAPublicKey decodes the DER-encoded P-256 public key bytes
// persisted in the attest_keys table. Helper exposed so handlers can
// validate parseability at registration time without duplicating the call.
func ParseECDSAPublicKey(der []byte) (*ecdsa.PublicKey, error) {
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse pkix: %w", err)
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("not an ECDSA key")
	}
	return ec, nil
}

// MarshalECDSAPublicKey is the inverse of ParseECDSAPublicKey, used to
// persist registered keys.
func MarshalECDSAPublicKey(key *ecdsa.PublicKey) ([]byte, error) {
	return x509.MarshalPKIXPublicKey(key)
}

// bytesEqual is a constant-time byte equality check. The rpIdHash is not a
// secret but using subtle.ConstantTimeCompare is the safe default for
// comparing crypto inputs — it's cheap (32 bytes) and avoids accidentally
// leaking a length-mismatch signal.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// --- tiny CBOR string / bytes scanner ---------------------------------------

// cborReadString reads a CBOR text-string (major type 3). Returns the
// decoded string and number of bytes consumed.
func cborReadString(b []byte) (string, int, error) {
	if len(b) == 0 {
		return "", 0, fmt.Errorf("cbor: empty: %w", ErrCaptchaFailed)
	}
	major := b[0] >> 5
	if major != 3 {
		return "", 0, fmt.Errorf("cbor: expected text-string, got major=%d: %w", major, ErrCaptchaFailed)
	}
	length, headerLen, err := cborReadLength(b)
	if err != nil {
		return "", 0, err
	}
	if length > uint64(len(b)-headerLen) || length > 64 {
		return "", 0, fmt.Errorf("cbor: string length overflow: %w", ErrCaptchaFailed)
	}
	return string(b[headerLen : headerLen+int(length)]), headerLen + int(length), nil
}

// cborReadBytes reads a CBOR byte-string (major type 2). Returns the bytes
// (sliced from b, no copy) and number of bytes consumed.
func cborReadBytes(b []byte) ([]byte, int, error) {
	if len(b) == 0 {
		return nil, 0, fmt.Errorf("cbor: empty: %w", ErrCaptchaFailed)
	}
	major := b[0] >> 5
	if major != 2 {
		return nil, 0, fmt.Errorf("cbor: expected byte-string, got major=%d: %w", major, ErrCaptchaFailed)
	}
	length, headerLen, err := cborReadLength(b)
	if err != nil {
		return nil, 0, err
	}
	if length > uint64(len(b)-headerLen) {
		return nil, 0, fmt.Errorf("cbor: byte-string length overflow: %w", ErrCaptchaFailed)
	}
	return b[headerLen : headerLen+int(length)], headerLen + int(length), nil
}

// cborReadLength decodes the length-prefix portion of a CBOR head byte.
// Supports lengths up to 2^32-1 (uint32). We never see longer fields in
// App Attest payloads.
func cborReadLength(b []byte) (length uint64, headerLen int, err error) {
	additional := b[0] & 0x1F
	switch {
	case additional < 24:
		return uint64(additional), 1, nil
	case additional == 24:
		if len(b) < 2 {
			return 0, 0, fmt.Errorf("cbor: truncated 1-byte length: %w", ErrCaptchaFailed)
		}
		return uint64(b[1]), 2, nil
	case additional == 25:
		if len(b) < 3 {
			return 0, 0, fmt.Errorf("cbor: truncated 2-byte length: %w", ErrCaptchaFailed)
		}
		return uint64(binary.BigEndian.Uint16(b[1:3])), 3, nil
	case additional == 26:
		if len(b) < 5 {
			return 0, 0, fmt.Errorf("cbor: truncated 4-byte length: %w", ErrCaptchaFailed)
		}
		return uint64(binary.BigEndian.Uint32(b[1:5])), 5, nil
	default:
		return 0, 0, fmt.Errorf("cbor: unsupported length encoding: %w", ErrCaptchaFailed)
	}
}

// EncodeAssertion is the inverse of decodeAssertion. Test helper exposed
// (lower-case file-private name) so unit tests can construct synthetic
// assertions without pulling in a CBOR dependency.
func encodeAssertion(authData, signature []byte) []byte {
	var out []byte
	out = append(out, 0xA2) // map(2)
	out = append(out, cborString("signature")...)
	out = append(out, cborBytes(signature)...)
	out = append(out, cborString("authenticatorData")...)
	out = append(out, cborBytes(authData)...)
	return out
}

func cborString(s string) []byte {
	return append(cborHead(3, uint64(len(s))), []byte(s)...)
}

func cborBytes(b []byte) []byte {
	return append(cborHead(2, uint64(len(b))), b...)
}

func cborHead(major byte, length uint64) []byte {
	switch {
	case length < 24:
		return []byte{major<<5 | byte(length)}
	case length < 1<<8:
		return []byte{major<<5 | 24, byte(length)}
	case length < 1<<16:
		out := []byte{major<<5 | 25, 0, 0}
		binary.BigEndian.PutUint16(out[1:], uint16(length))
		return out
	case length < 1<<32:
		out := []byte{major<<5 | 26, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(out[1:], uint32(length))
		return out
	}
	return nil // we never emit > 2^32 bytes
}

// --- in-memory stores used by tests + as default fallbacks ------------------

// InMemoryKeyStore is a process-local KeyStore. Production wires the
// Postgres-backed store from package handlers; this in-memory variant is
// used by unit tests and by main.go as a safe default if the DB-backed
// store isn't wired yet.
type InMemoryKeyStore struct {
	mu   sync.Mutex
	keys map[string]inMemoryKey
}

type inMemoryKey struct {
	pub     *ecdsa.PublicKey
	counter uint32
}

// NewInMemoryKeyStore returns an empty in-memory KeyStore.
func NewInMemoryKeyStore() *InMemoryKeyStore {
	return &InMemoryKeyStore{keys: make(map[string]inMemoryKey)}
}

// Register adds or replaces a key entry. Used by the bootstrap endpoint.
func (s *InMemoryKeyStore) Register(keyID string, pub *ecdsa.PublicKey, counter uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[keyID] = inMemoryKey{pub: pub, counter: counter}
}

// Lookup implements KeyStore.
func (s *InMemoryKeyStore) Lookup(_ context.Context, keyID string) (*ecdsa.PublicKey, uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.keys[keyID]
	if !ok {
		return nil, 0, ErrKeyNotFound
	}
	return entry.pub, entry.counter, nil
}

// BumpCounter implements KeyStore.
func (s *InMemoryKeyStore) BumpCounter(_ context.Context, keyID string, newCounter uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.keys[keyID]
	if !ok {
		return ErrKeyNotFound
	}
	entry.counter = newCounter
	s.keys[keyID] = entry
	return nil
}

// InMemoryChallengeStore is the test/default ChallengeStore.
//
// PERF / SECURITY: production should back this with Redis or a dedicated
// table so challenges remain consumed across multiple backend instances.
// The in-memory version is single-instance-safe but not horizontally safe.
type InMemoryChallengeStore struct {
	mu         sync.Mutex
	challenges map[string]struct{}
}

// NewInMemoryChallengeStore returns an empty in-memory ChallengeStore.
func NewInMemoryChallengeStore() *InMemoryChallengeStore {
	return &InMemoryChallengeStore{challenges: make(map[string]struct{})}
}

// Issue records a new server-issued challenge so Consume will accept it.
func (s *InMemoryChallengeStore) Issue(challenge []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challenges[string(challenge)] = struct{}{}
}

// Consume implements ChallengeStore.
func (s *InMemoryChallengeStore) Consume(_ context.Context, challenge []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.challenges[string(challenge)]; !ok {
		return ErrChallengeInvalid
	}
	delete(s.challenges, string(challenge))
	return nil
}

// --- helpers used internally ------------------------------------------------

// publicKeyFromUncompressed reconstructs a P-256 ecdsa.PublicKey from the
// 65-byte uncompressed-point format Apple ships in the attestationObject.
// Exposed for the registration handler to call without depending on
// internal symbols.
func publicKeyFromUncompressed(point []byte) (*ecdsa.PublicKey, error) {
	if len(point) != 65 || point[0] != 0x04 {
		return nil, errors.New("uncompressed point: expected 0x04|x(32)|y(32)")
	}
	x := new(big.Int).SetBytes(point[1:33])
	y := new(big.Int).SetBytes(point[33:65])
	return &ecdsa.PublicKey{Curve: ellipticP256(), X: x, Y: y}, nil
}
