package middleware

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
)

// CORS returns a configured CORS middleware.
// AllowCredentials is enabled, so AllowOrigins must NOT be "*".
// The origin is validated to prevent overly permissive configurations.
func CORS(origin string) fiber.Handler {
	// Reject wildcard origins when credentials are enabled -- this is
	// a security misconfiguration that browsers will block anyway.
	if strings.TrimSpace(origin) == "*" || origin == "" {
		origin = "http://localhost:3000"
	}

	return cors.New(cors.Config{
		AllowOrigins:     origin,
		AllowMethods:     "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization",
		AllowCredentials: true,
		MaxAge:           3600, // 1 hour -- reduce from 24h to limit stale policy risk.
	})
}
