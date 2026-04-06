package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds all application configuration.
type Config struct {
	SupabaseURL    string
	SupabaseAnonKey string
	JWTSecret      string
	Port           int
	CORSOrigin     string
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	supabaseURL := os.Getenv("SUPABASE_URL")
	if supabaseURL == "" {
		supabaseURL = "https://hceytkktkglzkcxlhpna.supabase.co"
	}

	supabaseAnonKey := os.Getenv("SUPABASE_ANON_KEY")
	if supabaseAnonKey == "" {
		return nil, fmt.Errorf("SUPABASE_ANON_KEY environment variable is required")
	}

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET environment variable is required")
	}

	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT: %w", err)
		}
		port = parsed
	}

	corsOrigin := os.Getenv("CORS_ORIGIN")
	if corsOrigin == "" {
		corsOrigin = "http://localhost:3000"
	}

	return &Config{
		SupabaseURL:    supabaseURL,
		SupabaseAnonKey: supabaseAnonKey,
		JWTSecret:      jwtSecret,
		Port:           port,
		CORSOrigin:     corsOrigin,
	}, nil
}
