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
		AllowMethods:     "GET,POST,PUT,PATCH,DELETE,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization,X-Timezone,If-None-Match",
		ExposeHeaders:    "ETag",
		AllowCredentials: true,
		// PERF: MaxAge = 24h caches the preflight decision in every modern
		// browser for a full day, so a single user can hit dozens of
		// cross-origin endpoints over a day with at most ~1 OPTIONS call
		// per origin/method. Chrome caps the header to 7200s (2h) in
		// practice, Firefox honors up to 24h; the header costs us nothing
		// in either case. Origin/method/header lists below only change at
		// deploy-time so there is no real "stale policy" risk.
		MaxAge: 86400,
	})
}
