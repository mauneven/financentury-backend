package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	_ "time/tzdata" // Embed IANA timezone database so time.LoadLocation works in minimal containers

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"github.com/joho/godotenv"

	"github.com/the-financial-workspace/backend/internal/config"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/handlers"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/routes"
	"github.com/the-financial-workspace/backend/internal/ws"
)

func main() {
	if err := run(); err != nil {
		log.Printf("fatal: %v", err)
		os.Exit(1)
	}
}

// run wires up the server and blocks on Listen. Errors are returned so main()
// can `os.Exit(1)` without skipping deferred cleanups (which `log.Fatalf` would).
func run() error {
	// Load .env file (ignore error if file doesn't exist).
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Load configuration.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize JWT validation.
	middleware.Init(cfg.JWTSecret)

	// Initialize Google OAuth handler with allowed redirect origins.
	// The frontend URL and CORS origin are both permitted as redirect targets.
	allowedOrigins := []string{cfg.FrontendURL}
	if cfg.CORSOrigin != cfg.FrontendURL {
		allowedOrigins = append(allowedOrigins, cfg.CORSOrigin)
	}
	handlers.InitAuth(cfg.GoogleClientID, cfg.GoogleClientSecret, allowedOrigins...)

	// Initialize invite handler with frontend URL.
	handlers.InitInvites(cfg.FrontendURL)

	// Initialize WebSocket hub for real-time updates.
	hub := ws.NewHub()
	go hub.Run()
	handlers.InitWebSocket(hub)
	log.Println("WebSocket hub started")

	// Initialize direct PostgreSQL connection pool. The database is used purely
	// as storage -- the backend enforces its own access control in Go handlers.
	database.Init(cfg.DatabaseURL)
	defer database.Close()
	log.Println("initialized database connection pool")

	// Start background expense pruner (runs hourly).
	handlers.StartExpensePruner()

	// Create Fiber app.
	fiberCfg := fiber.Config{
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			e := &fiber.Error{}
			if errors.As(err, &e) {
				code = e.Code
			}
			msg := err.Error()
			if code >= 500 {
				log.Printf("[error] request_id=%s method=%s path=%s err=%v",
					c.Locals("requestid"), c.Method(), c.Path(), err)
				msg = "internal server error"
			}
			return c.Status(code).JSON(fiber.Map{"error": msg})
		},
		AppName:   "Financial Workspace API",
		BodyLimit: 4 * 1024 * 1024, // 4MB (increased for migration payloads)
	}
	// Only enable proxy header parsing when explicit trusted CIDRs are configured.
	// Without this, c.IP() returns the direct connection IP (safe default).
	if len(cfg.TrustedProxies) > 0 {
		fiberCfg.EnableTrustedProxyCheck = true
		fiberCfg.TrustedProxies = cfg.TrustedProxies
		fiberCfg.ProxyHeader = "X-Forwarded-For"
	}
	app := fiber.New(fiberCfg)

	// Global middleware. Order matters:
	//   1. requestid — assigns a per-request ID first so every later
	//      middleware (including the logger and recover's panic trace) can
	//      attribute its output to the correct request.
	//   2. logger — wraps the request so latency/status are captured even
	//      when downstream middleware short-circuits or the handler panics.
	//   3. recover — converts panics into 500s before they unwind past
	//      compress/cors and kill the connection.
	//   4. compress — sits outside CORS so preflight/OPTIONS responses
	//      don't get gzipped (some clients mishandle compressed empty
	//      bodies) and inside logger so compression time is visible.
	//   5. cors — runs after compression setup; the handler itself still
	//      writes the Access-Control headers per request.
	// Auth middleware is attached per-route (protected group) rather than
	// globally, and cacheControlAndETag runs after the handler to stamp
	// Cache-Control / ETag onto the produced body.
	app.Use(requestid.New())
	app.Use(logger.New(logger.Config{
		Format:     "${time} | ${status} | ${latency} | ${method} ${path} | rid=${locals:requestid}\n",
		TimeFormat: "2006-01-02 15:04:05",
	}))
	app.Use(recover.New())
	// PERF: LevelBestSpeed. Our JSON payloads (summary / trends / list
	// endpoints) repeat identifiers and enum strings heavily, so even the
	// cheapest gzip level still hits a 4-6x ratio. LevelBestCompression
	// adds measurable CPU and latency with negligible ratio improvement;
	// LevelBestSpeed pairs better with the ETag / Cache-Control path added
	// below (304s and cache hits skip compression entirely).
	app.Use(compress.New(compress.Config{
		Level: compress.LevelBestSpeed,
	}))
	app.Use(middleware.CORS(cfg.CORSOrigin))

	// PERF: Cache-Control + ETag middleware.
	//
	// Runs after the handler has produced a JSON body. For GET requests on
	// endpoints that are safe to soft-cache (auth/me, summary, trends,
	// etc.) we:
	//   - attach `Cache-Control: private, max-age=10, stale-while-revalidate=30`
	//     so clients can skip us for 10s and asynchronously refresh for
	//     another 30s without a blocking hit.
	//   - compute a weak SHA-256 ETag over the response body. If the
	//     incoming request carries `If-None-Match: <etag>`, we return 304
	//     with an empty body — saving the full response size over the
	//     wire.
	//
	// The middleware is intentionally conservative: only GETs with 2xx
	// status and a JSON body are considered. Mutations always bypass
	// (responses may differ per-request via generated IDs).
	app.Use(cacheControlAndETag())

	// Security headers.
	app.Use(func(c *fiber.Ctx) error {
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("X-Frame-Options", "DENY")
		c.Set("X-XSS-Protection", "0") // Disabled in favor of CSP; legacy header can cause issues.
		c.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// CSP allows self + Google OAuth + inline styles (needed by chart libraries).
		c.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self' ws: wss: https://accounts.google.com https://oauth2.googleapis.com; font-src 'self'")
		return c.Next()
	})

	// Health check.
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":     "ok",
			"ws_clients": hub.ClientCount(),
		})
	})

	// Setup routes (including WebSocket).
	routes.Setup(app)

	// Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("shutting down server...")
		if err := app.Shutdown(); err != nil {
			log.Printf("server shutdown error: %v", err)
		}
	}()

	// Start server. Errors flow back through main() so deferred cleanup runs.
	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("server starting on %s", addr)
	if err := app.Listen(addr); err != nil {
		return fmt.Errorf("server error: %w", err)
	}
	return nil
}

// cacheablePathPrefixes lists the GET-endpoint prefixes we want to mark as
// soft-cacheable. Keep the list tight: anything whose response depends on
// non-idempotent state (e.g. WS broadcast fan-out, user-presence counters)
// must NOT be added here, or clients may serve stale data that the user
// perceives as a bug.
//
// PERF: The dashboard paths below typically re-fetch on every tab switch.
// A 10-second max-age turns those rapid-fire GETs into client-side hits,
// removing them from Supabase's query meter entirely.
var cacheablePathPrefixes = []string{
	"/api/auth/me",
	"/api/budgets", // covers list, :id, summary, trends, resume, categories, expenses, links, etc.
}

// isCacheablePath reports whether the request path should receive the
// Cache-Control + ETag treatment. It excludes the known-mutation-heavy
// subpaths of /api/budgets (invites, collaborators) so we don't serve stale
// access control data.
func isCacheablePath(path string) bool {
	for _, p := range cacheablePathPrefixes {
		if strings.HasPrefix(path, p) {
			// Subpaths that should always hit the DB fresh.
			if strings.Contains(path, "/invites") || strings.Contains(path, "/collaborators") {
				return false
			}
			return true
		}
	}
	return false
}

// cacheControlAndETag returns a Fiber middleware that:
//   - sets `Cache-Control: private, max-age=10, stale-while-revalidate=30`
//     on GET responses for cacheable paths.
//   - computes an ETag over the response body and short-circuits with 304
//     when If-None-Match matches.
//
// PERF: The 304 path is the big egress win. A fresh dashboard response is
// typically 30-200 KB after gzip; reducing it to a 0-byte 304 when nothing
// has changed takes that to near-zero.
func cacheControlAndETag() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Method() != fiber.MethodGet {
			return c.Next()
		}
		if !isCacheablePath(c.Path()) {
			return c.Next()
		}

		// Let the downstream handler run and fill in the body.
		if err := c.Next(); err != nil {
			return err
		}

		status := c.Response().StatusCode()
		if status < 200 || status >= 300 {
			return nil
		}

		body := c.Response().Body()
		if len(body) == 0 {
			return nil
		}

		// PERF: cap ETag computation at 256KB. These endpoints return small
		// JSON (summary, trends, resume, category/expense lists), so the
		// guard is a defense-in-depth fence: if a future change accidentally
		// streams a very large payload through this path we avoid blocking
		// the event loop on a SHA-256 over megabytes.
		const maxETagBody = 256 * 1024
		if len(body) > maxETagBody {
			return nil
		}

		// SECURITY: `private` keeps user-scoped payloads out of shared caches
		// (CDN / reverse proxy). `Vary: Authorization` prevents a shared
		// cache from serving user A's response to user B when both requests
		// differ only by token. Accept-Encoding ensures the ETag (computed
		// over the uncompressed body, before the compress middleware
		// unwinds) stays consistent with the encoding the client receives.
		c.Set("Cache-Control", "private, max-age=10, stale-while-revalidate=30")
		c.Set("Vary", "Authorization, Accept-Encoding")

		sum := sha256.Sum256(body)
		etag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
		c.Set("ETag", etag)

		// If-None-Match from the client may contain multiple comma-separated
		// etags; match if any of them equals ours.
		if inm := c.Get("If-None-Match"); inm != "" {
			for _, candidate := range strings.Split(inm, ",") {
				if strings.TrimSpace(candidate) == etag {
					// 304 Not Modified — strip the body.
					c.Status(fiber.StatusNotModified)
					c.Response().ResetBody()
					c.Response().Header.Del("Content-Length")
					return nil
				}
			}
		}
		return nil
	}
}
