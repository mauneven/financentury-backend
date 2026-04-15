package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
)

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

		// Store token hash for session tracking and revocation check.
		h := sha256.Sum256([]byte(tokenStr))
		tokenHash := hex.EncodeToString(h[:])
		c.Locals("token_hash", tokenHash)

		// Check if this token's session has been revoked. Also update
		// last_active_at lazily (fire-and-forget if stale > 5 min).
		if database.DB != nil {
			var revokedAt *time.Time
			var lastActive time.Time
			var sessionID string
			err := database.DB.Pool.QueryRow(context.Background(),
				`SELECT id, revoked_at, last_active_at FROM user_sessions WHERE token_hash = $1`,
				tokenHash,
			).Scan(&sessionID, &revokedAt, &lastActive)
			if err == nil {
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
