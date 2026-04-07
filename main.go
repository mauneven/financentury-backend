package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/joho/godotenv"
	"github.com/the-financial-workspace/backend/internal/config"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/routes"
)

func main() {
	// Load .env file (ignore error if file doesn't exist).
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Load configuration.
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Initialize JWT secret.
	middleware.SetJWTSecret(cfg.JWTSecret)

	// Initialize Supabase REST API client.
	database.Init(cfg.SupabaseURL, cfg.SupabaseAnonKey)
	defer database.Close()
	log.Printf("initialized Supabase client for %s", cfg.SupabaseURL)

	// Create Fiber app.
	app := fiber.New(fiber.Config{
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			code := fiber.StatusInternalServerError
			if e, ok := err.(*fiber.Error); ok {
				code = e.Code
			}
			msg := err.Error()
			if code >= 500 {
				log.Printf("internal error: %v", err)
				msg = "internal server error"
			}
			return c.Status(code).JSON(fiber.Map{"error": msg})
		},
		AppName:   "Financial Workspace API",
		BodyLimit: 64 * 1024, // 64KB
	})

	// Global middleware.
	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format:     "${time} | ${status} | ${latency} | ${method} ${path}\n",
		TimeFormat: "2006-01-02 15:04:05",
	}))
	app.Use(middleware.CORS(cfg.CORSOrigin))

	// Health check.
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	// Setup routes.
	routes.Setup(app)

	// Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("shutting down server...")
		if err := app.Shutdown(); err != nil {
			log.Fatalf("server shutdown error: %v", err)
		}
	}()

	// Start server.
	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("server starting on %s", addr)
	if err := app.Listen(addr); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
