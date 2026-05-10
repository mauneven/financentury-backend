package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/the-financial-workspace/backend/internal/database"
)

// sessionCacheEntry holds cached session revocation state to avoid hitting the
// DB on every authenticated request.
type sessionCacheEntry struct {
	revokedAt *time.Time
	sessionID string
	userID    string
	cachedAt  time.Time
}

// sessionCache is a package-level in-memory cache keyed by token hash.
//
// PERF: This cache is the single biggest cost-saver in the request path.
// Every authenticated request runs through Protected(), so a miss means an
// extra round-trip to Postgres on top of the actual handler's queries. At
// ~100+ requests/min per active user that adds up quickly on Supabase's
// metered connections. The cache turns that into at most one DB query per
// token per sessionCacheTTL.
var sessionCache sync.Map

// sessionCacheTTL is how long a cache entry is considered fresh.
//
// PERF: Bumped from 60s to 5min. Revocation is explicit (SignOut,
// RevokeSession, DeleteAccount all call InvalidateSessionCache /
// InvalidateUserSessionCache) so a longer TTL is safe: a revoked token is
// booted immediately, and a stale-but-valid token was already accepted for
// the full 7-day JWT lifetime anyway. The 5-minute ceiling only affects
// delayed discovery of out-of-band session state changes (e.g. admin-issued
// DB updates), which is acceptable.
const sessionCacheTTL = 5 * time.Minute

// sessionCacheMaxAge is the maximum time an entry may remain in the cache
// (even if re-queried recently). Used by the background evictor to bound
// memory growth from long-lived or abandoned tokens.
const sessionCacheMaxAge = 30 * time.Minute

// InvalidateSessionCache removes a cached session entry so the next request
// for that token will re-query the database. Call this when revoking a session.
func InvalidateSessionCache(tokenHash string) {
	sessionCache.Delete(tokenHash)
}

// InvalidateUserSessionCache removes all cached session entries for a given
// user so that any subsequent request using those tokens is re-validated
// against the database. Call this when deleting a user or wiping all their
// sessions.
func InvalidateUserSessionCache(userID string) {
	if userID == "" {
		return
	}
	sessionCache.Range(func(key, value interface{}) bool {
		entry, ok := value.(sessionCacheEntry)
		if !ok {
			return true
		}
		if entry.userID == userID {
			sessionCache.Delete(key)
		}
		return true
	})
}

// startSessionCacheEvictor launches a background goroutine that periodically
// purges expired entries from sessionCache, bounding memory growth.
//
// SECURITY: this is the only mechanism bounding cache size. Entries are
// evicted strictly by max-age (sessionCacheMaxAge), independent of TTL, so
// abandoned-but-valid tokens cannot sit in memory indefinitely. Store-side
// flooding is also mitigated upstream: entries are only written after a
// successful JWT signature verification *and* a matching user_sessions row,
// so an attacker without the signing key cannot inflate the cache.
func startSessionCacheEvictor() {
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			sessionCache.Range(func(key, value interface{}) bool {
				entry, ok := value.(sessionCacheEntry)
				if !ok {
					sessionCache.Delete(key)
					return true
				}
				if now.Sub(entry.cachedAt) > sessionCacheMaxAge {
					sessionCache.Delete(key)
				}
				return true
			})
		}
	}()
}

func init() {
	startSessionCacheEvictor()
}

// Claims represents the JWT claims issued by the backend.
type Claims struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

// jwtSecret holds the secret used to sign and validate tokens.
var jwtSecret []byte

// Init configures the middleware package with the JWT secret.
func Init(secret string) {
	jwtSecret = []byte(secret)
}

// Protected is a Fiber middleware that validates a backend-issued JWT from the
// Authorization header. On success it stores the user_id (uuid.UUID) and
// email (string) in Fiber locals for downstream handlers.
func Protected() fiber.Handler {
	return func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "missing authorization header",
			})
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid authorization header format",
			})
		}

		tokenStr := parts[1]
		claims := &Claims{}

		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return jwtSecret, nil
		}, jwt.WithIssuer("financial-workspace"))

		if err != nil || !token.Valid {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid or expired token",
			})
		}

		// Parse the user_id claim as a UUID.
		userID, err := uuid.Parse(claims.UserID)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid user id in token",
			})
		}

		// SECURITY: cache key is sha256(entire JWT). Any modification to
		// header/payload/signature changes the hash, so an attacker who
		// flips the user_id claim cannot reuse another user's cache entry
		// — the forged token produces a different key that misses the
		// cache and hits the DB path, where signature validation has
		// already rejected it above.
		h := sha256.Sum256([]byte(tokenStr))
		tokenHash := hex.EncodeToString(h[:])
		c.Locals("token_hash", tokenHash)

		// PERF: Check if this token's session has been revoked. Uses an
		// in-memory TTL cache (sessionCacheTTL = 5 min) so the DB query runs
		// at most once per 5 min per session. Revocation endpoints
		// (SignOut, RevokeSession, DeleteAccount) call InvalidateSessionCache
		// / InvalidateUserSessionCache to boot tokens from the cache
		// immediately, so a longer TTL does not weaken the revocation
		// guarantee. last_active_at is also updated lazily (fire-and-forget
		// if stale > 5 min) so reads don't incur a write round-trip every
		// request.
		if database.DB != nil {
			// Try the cache first. Fast path: no DB hit.
			if cached, ok := sessionCache.Load(tokenHash); ok {
				entry := cached.(sessionCacheEntry)
				// SECURITY: enforce TTL on cache hits. Expired entries fall
				// through to the DB path; we never serve a stale entry on a
				// miss — revocation state could have flipped since cachedAt.
				if time.Since(entry.cachedAt) < sessionCacheTTL {
					if entry.revokedAt != nil {
						return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
							"error": "session has been revoked",
						})
					}
					// SECURITY: re-check the cached session's user_id against
					// the token's claim on every cache hit. Without this an
					// attacker who possesses a valid signing key (or a token
					// hash collision — infeasible for sha256, but defense in
					// depth) could rebind a cached entry to a different user
					// by crafting a JWT with a different user_id claim.
					// Identical check to the miss-path below; we repeat it
					// here so the cache path is never weaker than the DB path.
					if entry.userID != "" && entry.userID != userID.String() {
						return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
							"error": "session user mismatch",
						})
					}
					// Cache hit and not revoked — skip DB query entirely.
					c.Locals("user_id", userID)
					c.Locals("email", claims.Email)
					return c.Next()
				}
			}

			var revokedAt *time.Time
			var lastActive time.Time
			var sessionID string
			var sessionUserID string
			err := database.DB.Pool.QueryRow(context.Background(),
				`SELECT id, user_id::text, revoked_at, last_active_at FROM user_sessions WHERE token_hash = $1`,
				tokenHash,
			).Scan(&sessionID, &sessionUserID, &revokedAt, &lastActive)
			if err == nil {
				// SECURITY: ensure the token's user_id claim matches the
				// user_id stored with the session row. Without this check a
				// token rebound to a different user (via a custom JWT with
				// the correct signing key, e.g. during an incident response
				// where the key leaked) could still authenticate. Mirrored
				// on the cache-hit path above.
				if sessionUserID != userID.String() {
					return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
						"error": "session user mismatch",
					})
				}

				// Store result in cache.
				sessionCache.Store(tokenHash, sessionCacheEntry{
					revokedAt: revokedAt,
					sessionID: sessionID,
					userID:    sessionUserID,
					cachedAt:  time.Now(),
				})

				if revokedAt != nil {
					return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
						"error": "session has been revoked",
					})
				}
				if time.Since(lastActive) > 5*time.Minute {
					go func() {
						_, _ = database.DB.Pool.Exec(context.Background(),
							`UPDATE user_sessions SET last_active_at = NOW() WHERE id = $1`, sessionID)
					}()
				}
			}
			// err != nil means no session row — backward compatible, allow.
		}

		c.Locals("user_id", userID)
		c.Locals("email", claims.Email)
		return c.Next()
	}
}

// GenerateToken creates a signed JWT with 7-day expiry for the given user.
// Includes issuer and subject claims for additional validation.
func GenerateToken(userID uuid.UUID, email string) (string, error) {
	now := time.Now()
	claims := &Claims{
		UserID: userID.String(),
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(7 * 24 * time.Hour)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// JWTSecret returns the signing key so that other packages (e.g. WebSocket
// authentication) can validate tokens without duplicating the secret storage.
func JWTSecret() []byte {
	return jwtSecret
}

// GetUserID extracts the user ID from the Fiber context.
func GetUserID(c *fiber.Ctx) uuid.UUID {
	userID, ok := c.Locals("user_id").(uuid.UUID)
	if !ok {
		return uuid.Nil
	}
	return userID
}
