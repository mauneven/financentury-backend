package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all application configuration.
type Config struct {
	SupabaseURL        string
	SupabaseAnonKey    string
	JWTSecret          string
	GoogleClientID     string
	GoogleClientSecret string
	FrontendURL        string
	Port               int
	CORSOrigin         string
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	supabaseURL := os.Getenv("SUPABASE_URL")
	if supabaseURL == "" {
		return nil, fmt.Errorf("SUPABASE_URL environment variable is required")
	}

	supabaseAnonKey := os.Getenv("SUPABASE_ANON_KEY")
	if supabaseAnonKey == "" {
		return nil, fmt.Errorf("SUPABASE_ANON_KEY environment variable is required")
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET environment variable is required")
	}
	if len(jwtSecret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be at least 32 characters long for adequate security")
	}

	googleClientID := os.Getenv("GOOGLE_CLIENT_ID")
	if googleClientID == "" {
		return nil, fmt.Errorf("GOOGLE_CLIENT_ID environment variable is required")
	}

	googleClientSecret := os.Getenv("GOOGLE_CLIENT_SECRET")
	if googleClientSecret == "" {
		return nil, fmt.Errorf("GOOGLE_CLIENT_SECRET environment variable is required")
	}

	frontendURL := os.Getenv("FRONTEND_URL")
	if frontendURL == "" {
		frontendURL = "http://localhost:3000"
	}

	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT: %w", err)
		}
		port = parsed
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("PORT must be between 1 and 65535, got %d", port)
	}

	corsOrigin := os.Getenv("CORS_ORIGIN")
	if corsOrigin == "" {
		corsOrigin = "http://localhost:3000"
	}

	return &Config{
		SupabaseURL:        supabaseURL,
		SupabaseAnonKey:    supabaseAnonKey,
		JWTSecret:          jwtSecret,
		GoogleClientID:     googleClientID,
		GoogleClientSecret: googleClientSecret,
		FrontendURL:        frontendURL,
		Port:               port,
		CORSOrigin:         corsOrigin,
	}, nil
}
