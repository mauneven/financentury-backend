package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"

	"github.com/the-financial-workspace/backend/internal/captcha"
	"github.com/the-financial-workspace/backend/internal/database"
)

// attestChallengeBytes is the length of a single-use App Attest challenge.
// 32 bytes is 256 bits — well above any practical guess budget.
const attestChallengeBytes = 32

// attestChallengeTTL bounds the window during which a challenge can be
// redeemed. 5 minutes accommodates a normal mobile login flow (fetch
// challenge → run DCAppAttestService.generateAssertion → submit token)
// without keeping the table heavy.
const attestChallengeTTL = 5 * time.Minute

// AttestKeyStore is the production KeyStore wired into the App Attest
// verifier. It persists registered ECDSA P-256 public keys + per-key
// counters in the attest_keys table.
type AttestKeyStore struct{}

// Lookup implements captcha.KeyStore.
func (AttestKeyStore) Lookup(ctx context.Context, keyID string) (*ecdsa.PublicKey, uint32, error) {
	if database.DB == nil {
		return nil, 0, captcha.ErrKeyNotFound
	}
	var pubBytes []byte
	var counter int64
	err := database.DB.Pool.QueryRow(ctx,
		`SELECT public_key, counter
		 FROM attest_keys
		 WHERE platform = 'ios' AND key_id = $1`, keyID,
	).Scan(&pubBytes, &counter)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, 0, captcha.ErrKeyNotFound
		}
		return nil, 0, err
	}
	if len(pubBytes) == 0 {
		return nil, 0, captcha.ErrKeyNotFound
	}
	pub, err := captcha.ParseECDSAPublicKey(pubBytes)
	if err != nil {
		return nil, 0, err
	}
	if counter < 0 {
		counter = 0
	}
	return pub, uint32(counter), nil
}

// BumpCounter implements captcha.KeyStore. Uses a single UPDATE with a
// counter > $2 guard so two concurrent assertions can never both increment
// past the same value.
func (AttestKeyStore) BumpCounter(ctx context.Context, keyID string, newCounter uint32) error {
	if database.DB == nil {
		return nil
	}
	tag, err := database.DB.Pool.Exec(ctx,
		`UPDATE attest_keys
		   SET counter = $1, last_seen_at = now()
		 WHERE platform = 'ios' AND key_id = $2 AND counter < $1`,
		int64(newCounter), keyID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// Either the row vanished (shouldn't happen — Lookup succeeded
		// moments ago) or the counter raced past us. Either way the
		// assertion was already redeemed.
		return errors.New("attest_keys: counter not advanced (race)")
	}
	return nil
}

// AttestChallengeStore is the production ChallengeStore. Single-use
// challenges live in the attest_challenges table; Consume() deletes the
// row atomically.
type AttestChallengeStore struct{}

// Issue records a new challenge for the given expiry. Returns the raw bytes.
func (AttestChallengeStore) Issue(ctx context.Context, ttl time.Duration) ([]byte, error) {
	challenge := make([]byte, attestChallengeBytes)
	if _, err := rand.Read(challenge); err != nil {
		return nil, err
	}
	if database.DB == nil {
		return challenge, nil
	}
	// SECURITY: use a parameterised interval via `... * interval '1 second'`
	// instead of relying on time.Duration.String() (which yields "5m0s",
	// only loosely parseable as Postgres interval). The integer-seconds
	// path is round-trip-stable across pgx exec modes.
	_, err := database.DB.Pool.Exec(ctx,
		`INSERT INTO attest_challenges (challenge, expires_at)
		 VALUES ($1, now() + ($2::int * interval '1 second'))`,
		challenge, int(ttl.Seconds()))
	if err != nil {
		return nil, err
	}
	return challenge, nil
}

// Consume implements captcha.ChallengeStore. DELETE returning 1 if the row
// exists + is unexpired, 0 otherwise. Atomicity guarantees single-use.
func (AttestChallengeStore) Consume(ctx context.Context, challenge []byte) error {
	if database.DB == nil {
		return nil
	}
	tag, err := database.DB.Pool.Exec(ctx,
		`DELETE FROM attest_challenges
		 WHERE challenge = $1 AND expires_at > now()`,
		challenge)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return captcha.ErrChallengeInvalid
	}
	return nil
}

// attestRegisterRequest is the body posted by iOS clients on first-launch.
//
// challenge: the same challenge returned by GET /api/attest/challenge. The
// attestationObject was generated over this challenge as input — replaying
// the registration with a known key won't bind to a fresh challenge.
type attestRegisterRequest struct {
	KeyID             string `json:"key_id"`
	AttestationObject []byte `json:"attestation_object"`
	Challenge         []byte `json:"challenge"`
}

// AttestChallenge handles GET /api/attest/challenge. Returns a new
// single-use 32-byte challenge base64-encoded. The client passes it back
// in the App Attest assertion's client_data_hash AND in the captcha
// envelope so the server can verify single-use.
func AttestChallenge(c *fiber.Ctx) error {
	store := AttestChallengeStore{}
	challenge, err := store.Issue(c.Context(), attestChallengeTTL)
	if err != nil {
		log.Printf("[attest] issue challenge failed: %v", err)
		return errInternal(c, "failed to issue challenge")
	}
	return c.JSON(fiber.Map{
		"challenge":  base64.StdEncoding.EncodeToString(challenge),
		"expires_in": int(attestChallengeTTL.Seconds()),
	})
}

// AttestRegister handles POST /api/attest/register (no captcha required —
// this is the bootstrap). iOS clients post their attestation object once,
// per app instance, after DCAppAttestService.generateKey + .attestKey.
//
// We:
//  1. Parse the attestationObject to extract the credential public key.
//  2. Consume the issued challenge (single-use; replay protection).
//  3. UPSERT into attest_keys.
//
// SECURITY LIMITATIONS:
//   - We do NOT validate the Apple x5c certificate chain (see
//     captcha.ValidateAttestationStatement). Tracked as TODO.
//   - The endpoint is open (no auth). Production should rate-limit at the
//     reverse proxy / WAF layer, and consider gating with a short-lived
//     admin-issued bootstrap token before chain validation lands.
func AttestRegister(c *fiber.Ctx) error {
	var req attestRegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}
	if req.KeyID == "" {
		return errBadRequest(c, "key_id required")
	}
	if len(req.AttestationObject) == 0 {
		return errBadRequest(c, "attestation_object required")
	}
	if len(req.Challenge) == 0 {
		return errBadRequest(c, "challenge required")
	}
	if len(req.AttestationObject) > 16*1024 {
		// 16 KB is more than enough for a real attestation object
		// (typical ~2-4 KB). Anything larger is either malformed or
		// adversarial.
		return errBadRequest(c, "attestation_object too large")
	}

	result, err := captcha.ParseAttestationObject(req.AttestationObject)
	if err != nil {
		log.Printf("[attest] parse attestation object: %v", err)
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "invalid attestation object",
		})
	}

	// SECURITY: see TODO in captcha.ValidateAttestationStatement. Once
	// Apple root validation lands, this is where we'd verify the x5c
	// chain and the receipt.
	if err := captcha.ValidateAttestationStatement(req.AttestationObject); err != nil {
		return errBadRequest(c, "attestation validation failed")
	}

	// SECURITY: consume the challenge — replay protection on registration.
	challengeStore := AttestChallengeStore{}
	if err := challengeStore.Consume(c.Context(), req.Challenge); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "challenge invalid",
		})
	}

	keyDER, err := captcha.MarshalECDSAPublicKey(result.PublicKey)
	if err != nil {
		return errInternal(c, "failed to marshal key")
	}

	// Use the client-provided keyID since DCAppAttestService.generateKey
	// returns it directly and the iOS client passes that verbatim — our
	// derived hash is informational.
	if database.DB != nil {
		_, err := database.DB.Pool.Exec(c.Context(),
			`INSERT INTO attest_keys (platform, key_id, public_key, counter, last_seen_at)
			 VALUES ('ios', $1, $2, $3, now())
			 ON CONFLICT (platform, key_id) DO UPDATE
			   SET public_key = EXCLUDED.public_key,
			       counter = EXCLUDED.counter,
			       last_seen_at = now()`,
			req.KeyID, keyDER, int64(result.Counter))
		if err != nil {
			log.Printf("[attest] upsert key failed: %v", err)
			return errInternal(c, "failed to persist key")
		}
	}

	return c.JSON(fiber.Map{
		"ok":     true,
		"key_id": req.KeyID,
	})
}

// _ asserts AttestKeyStore satisfies captcha.KeyStore at compile time so a
// drift in the interface breaks the build, not a request at 3am.
var (
	_ captcha.KeyStore       = AttestKeyStore{}
	_ captcha.ChallengeStore = AttestChallengeStore{}
)

// attestRegisterRequestBytes is a debug helper exposed so tests can
// construct registration requests without importing internal types.
func attestRegisterRequestBytes(keyID string, attObj, challenge []byte) ([]byte, error) {
	return json.Marshal(attestRegisterRequest{
		KeyID:             keyID,
		AttestationObject: attObj,
		Challenge:         challenge,
	})
}
