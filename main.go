package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // Embed IANA timezone database so time.LoadLocation works in minimal containers

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"github.com/joho/godotenv"

	"github.com/the-financial-workspace/backend/internal/captcha"
	"github.com/the-financial-workspace/backend/internal/config"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/handlers"
	"github.com/the-financial-workspace/backend/internal/middleware"
	rediscache "github.com/the-financial-workspace/backend/internal/redis"
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

	// PERF: optional Redis cache-aside layer for read-mostly endpoints.
	// REDIS_URL empty / unreachable -> no-op cache. Never required for correctness.
	rediscache.SetDefault(rediscache.New(cfg.RedisURL))
	defer rediscache.CloseDefault()

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
	// PERF: Fiber's compress middleware delegates to fasthttp's
	// CompressHandlerBrotliLevel, which negotiates Accept-Encoding in this
	// preference order: br > gzip > deflate > zstd > identity. Modern
	// clients (Chromium >= 50, Firefox >= 44, Safari >= 14, every current
	// curl) advertise `br` and pick up brotli automatically; older clients
	// fall back to gzip; ancient clients land on identity. Brotli typically
	// gives ~15-25% better ratio than gzip on JSON, so the bandwidth bill
	// drops without any client-side change.
	//
	// LevelBestSpeed maps to CompressBrotliBestSpeed for brotli AND
	// CompressBestSpeed for gzip/deflate. Our JSON payloads repeat
	// identifiers / enum strings heavily, so even the cheapest level still
	// hits a 4-6x ratio for gzip and a slightly better one for brotli.
	// LevelBestCompression adds CPU and latency with negligible ratio
	// improvement; LevelBestSpeed pairs better with the ETag / 304 path
	// (those skip compression entirely).
	app.Use(compress.New(compress.Config{
		Level: compress.LevelBestSpeed,
	}))
	app.Use(middleware.CORS(cfg.CORSOrigin))

	// SECURITY: tighter body limits on the auth surface. The global
	// BodyLimit (4 MB) is sized for migration payloads; auth requests
	// only ever carry a Google ID token + small profile patch, so we cap
	// at 64 KB to shrink the DoS / abuse surface. We enforce this with a
	// pre-handler middleware mounted only on /api/auth/* — Fiber's
	// global BodyLimit can't be tightened per-route otherwise.
	app.Use("/api/auth", maxBodySize(64*1024))

	// PERF: Cache-Control + ETag middleware.
	//
	// Runs after the handler has produced a JSON body. For GET requests on
	// soft-cacheable endpoints (auth/me, summary, trends, etc.) we:
	//   - attach a per-endpoint Cache-Control directive with stale-while-
	//     revalidate + stale-if-error so clients keep working through
	//     transient backend hiccups without blocking on us.
	//   - compute a weak xxhash64 ETag over the response body. If the
	//     incoming request carries `If-None-Match: <etag>`, we return 304
	//     with an empty body — saving the full response size over the
	//     wire.
	//
	// The middleware is intentionally conservative: only GETs with 2xx
	// status and a JSON body are considered. Mutations always bypass
	// (responses may differ per-request via generated IDs). The directive
	// table lives in internal/middleware/cache.go.
	app.Use(middleware.CacheControlAndETag())

	// Security headers.
	app.Use(func(c *fiber.Ctx) error {
		c.Set("X-Content-Type-Options", "nosniff")
		c.Set("X-Frame-Options", "DENY")
		c.Set("X-XSS-Protection", "0") // Disabled in favor of CSP; legacy header can cause issues.
		c.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		// SECURITY: HSTS pins clients to HTTPS for 2 years, applies to
		// every subdomain, and opts in to the Chromium / Firefox preload
		// list. Set unconditionally so reverse proxies / Render / Fly /
		// Railway don't have to remember to add it. The header is a
		// no-op when served over plain HTTP locally.
		c.Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains; preload")
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

	// SECURITY: bot protection. Each platform's verifier is a no-op when
	// its secret is missing — dev/staging without secrets stays
	// functional. We log a warning per disabled platform so prod
	// operators notice.
	turnstileV := captcha.NewTurnstile(cfg.TurnstileSecret)
	if reason := captcha.Reason(turnstileV); reason != "" {
		log.Printf("[captcha] WARNING: %s", reason)
	}
	appleV := captcha.NewAppleAppAttest(
		cfg.AppleAppAttestTeamID,
		cfg.AppleAppAttestBundleID,
		handlers.AttestKeyStore{},
		handlers.AttestChallengeStore{},
	)
	if reason := captcha.Reason(appleV); reason != "" {
		log.Printf("[captcha] WARNING: %s", reason)
	}
	playV := captcha.NewGooglePlayIntegrity(
		cfg.GooglePlayIntegrityPackageName,
		cfg.GooglePlayIntegrityDecryptionKey,
		cfg.GooglePlayIntegrityVerificationKey,
		handlers.AttestChallengeStore{},
	)
	if reason := captcha.Reason(playV); reason != "" {
		log.Printf("[captcha] WARNING: %s", reason)
	}
	captchaV := captcha.NewMulti(captcha.MultiConfig{
		Web:     turnstileV,
		IOS:     appleV,
		Android: playV,
	})

	// Setup routes (including WebSocket).
	routes.SetupWith(app, routes.SetupConfig{Captcha: captchaV})

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

// maxBodySize returns a middleware that enforces a per-request body size
// cap, independent of the Fiber app-level BodyLimit. We need this to apply
// stricter limits on /api/auth/* (a 64 KB Google ID token + profile patch
// will never come close) than on /api/migrate (which carries the user's
// entire export and uses the global 4 MB limit).
//
// We trust Content-Length when present and fall back to inspecting the
// post-read body for chunked transfers — the latter still rejects oversized
// uploads but only after the bytes have been received, which is acceptable
// for non-streaming API requests.
func maxBodySize(limit int) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cl := c.Request().Header.ContentLength(); cl > limit {
			return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{
				"error": "request body too large",
			})
		}
		// Defense in depth: chunked transfers (no Content-Length) reach
		// here with the full body already read by fasthttp. Reject if it
		// blew past the cap.
		if len(c.Body()) > limit {
			return c.Status(fiber.StatusRequestEntityTooLarge).JSON(fiber.Map{
				"error": "request body too large",
			})
		}
		return c.Next()
	}
}
