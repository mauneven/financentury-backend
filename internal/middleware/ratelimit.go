// Package middleware provides Fiber middlewares used across the API.
//
// PERF: The rate limiters in this file use Fiber's default in-memory storage
// (no storage backend configured on limiter.Config.Storage), which means
// counters live in process memory only. No Supabase / Redis round-trip on
// the hot path. Do NOT add a remote Storage here without first profiling —
// a rate limiter that talks to the DB trades linear DB query cost for
// something we were explicitly trying to reduce.
package middleware

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/google/uuid"
)

// AuthRateLimiter returns a rate limiter for authentication endpoints.
// Allows 10 requests per minute per IP to prevent brute-force attacks.
func AuthRateLimiter() fiber.Handler {
	return limiter.New(limiter.Config{
		// PERF: in-memory counter store (default). No DB calls per request.
		Max:        10,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "too many requests, please try again later",
			})
		},
	})
}

// APIRateLimiter returns a rate limiter for general API endpoints.
// Allows 100 requests per minute per authenticated user. This middleware is
// always mounted AFTER Protected(), so c.Locals("user_id") is populated. If a
// request ever slips through without authentication (e.g. middleware ordering
// regression), we fall back to keying on the client IP so that the limiter
// still bounds that caller rather than silently collapsing every anonymous
// request onto a single shared counter.
func APIRateLimiter() fiber.Handler {
	return limiter.New(limiter.Config{
		// PERF: in-memory counter store (default). No DB calls per request.
		Max:        100,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			if uid := GetUserID(c); uid != uuid.Nil {
				return "user:" + uid.String()
			}
			return "ip:" + c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "too many requests, please try again later",
			})
		},
	})
}

// MigrateRateLimiter returns a strict rate limiter for the migration endpoint.
// Allows 5 requests per minute per IP since migrations are heavy operations.
func MigrateRateLimiter() fiber.Handler {
	return limiter.New(limiter.Config{
		// PERF: in-memory counter store (default). No DB calls per request.
		Max:        5,
		Expiration: 1 * time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error": "too many requests, please try again later",
			})
		},
	})
}
