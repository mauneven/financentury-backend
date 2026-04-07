package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// SupabaseClaims matches the JWT claims issued by Supabase Auth.
type SupabaseClaims struct {
	Sub   string `json:"sub"`
	Email string `json:"email"`
	Role  string `json:"role"`
	Aud   string `json:"aud"`
	jwt.RegisteredClaims
}

// jwtSecret holds the Supabase JWT secret used to validate tokens.
var jwtSecret []byte

// Init configures the middleware package with the Supabase JWT secret.
func Init(supabaseJWTSecret string) {
	jwtSecret = []byte(supabaseJWTSecret)
}

// Protected is a Fiber middleware that validates a Supabase JWT from the
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
		claims := &SupabaseClaims{}

		token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, jwt.ErrSignatureInvalid
			}
			return jwtSecret, nil
		})

		if err != nil || !token.Valid {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid or expired token",
			})
		}

		// Parse the sub claim as a UUID (Supabase user ID).
		userID, err := uuid.Parse(claims.Sub)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "invalid user id in token",
			})
		}

		c.Locals("user_id", userID)
		c.Locals("email", claims.Email)
		return c.Next()
	}
}

// GetUserID extracts the user ID from the Fiber context.
func GetUserID(c *fiber.Ctx) uuid.UUID {
	userID, ok := c.Locals("user_id").(uuid.UUID)
	if !ok {
		return uuid.Nil
	}
	return userID
}
