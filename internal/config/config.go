package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all application configuration.
type Config struct {
	DatabaseURL        string
	JWTSecret          string
	GoogleClientID     string
	GoogleClientSecret string
	FrontendURL        string
	Port               int
	CORSOrigin         string
	TrustedProxies     []string
	// RedisURL is optional. When empty the cache-aside layer falls back to a
	// no-op implementation (see internal/redis). PERF only — never required
	// for correctness.
	RedisURL string

	// --- Bot protection (all optional — empty → no-op verifier). ---
	// SECURITY: leaving any of these empty disables the corresponding
	// platform's captcha check. main.go logs a startup warning so the
	// state is observable. Production should set TurnstileSecret +
	// either the Apple or Play Integrity bundle depending on which
	// mobile client(s) are shipping.

	// TurnstileSecret is the Cloudflare Turnstile siteverify secret key
	// (begins with 0x4A...). Source: Cloudflare dashboard → Turnstile
	// widget → "secret key" tab. Empty disables web captcha.
	TurnstileSecret string

	// AppleAppAttestTeamID is the Apple Developer Team ID (10-character
	// alphanumeric). Source: Apple Developer Portal → Membership.
	AppleAppAttestTeamID string

	// AppleAppAttestBundleID is the iOS app bundle identifier
	// (e.g. com.financentury.app). Must match the binary signed for the
	// App Attest service.
	AppleAppAttestBundleID string

	// GooglePlayIntegrityPackageName is the Android app package
	// identifier (must match google-services.json + Play Console).
	GooglePlayIntegrityPackageName string

	// GooglePlayIntegrityDecryptionKey is the base64-encoded AES-256-KW
	// key used to unwrap the Play Integrity JWE. Source: Play Console →
	// "Setup integrity in Play Console" → "Manage decryption keys".
	GooglePlayIntegrityDecryptionKey string

	// GooglePlayIntegrityVerificationKey is the base64-encoded ECDSA
	// P-256 public key used to verify the inner JWS signature.
	GooglePlayIntegrityVerificationKey string
}

// Load reads configuration from environment variables.
func Load() (*Config, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL environment variable is required")
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

	port := 3000
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

	// TRUSTED_PROXIES: comma-separated CIDRs of reverse proxies (e.g. load balancer).
	// When empty, proxy header checking is disabled entirely (safe default).
	var trustedProxies []string
	if tp := os.Getenv("TRUSTED_PROXIES"); tp != "" {
		for _, cidr := range strings.Split(tp, ",") {
			cidr = strings.TrimSpace(cidr)
			if cidr != "" {
				trustedProxies = append(trustedProxies, cidr)
			}
		}
	}

	return &Config{
		DatabaseURL:                        databaseURL,
		JWTSecret:                          jwtSecret,
		GoogleClientID:                     googleClientID,
		GoogleClientSecret:                 googleClientSecret,
		FrontendURL:                        frontendURL,
		Port:                               port,
		CORSOrigin:                         corsOrigin,
		TrustedProxies:                     trustedProxies,
		RedisURL:                           strings.TrimSpace(os.Getenv("REDIS_URL")),
		TurnstileSecret:                    strings.TrimSpace(os.Getenv("TURNSTILE_SECRET")),
		AppleAppAttestTeamID:               strings.TrimSpace(os.Getenv("APPLE_APP_ATTEST_TEAM_ID")),
		AppleAppAttestBundleID:             strings.TrimSpace(os.Getenv("APPLE_APP_ATTEST_BUNDLE")),
		GooglePlayIntegrityPackageName:     strings.TrimSpace(os.Getenv("GOOGLE_PLAY_INTEGRITY_PACKAGE_NAME")),
		GooglePlayIntegrityDecryptionKey:   strings.TrimSpace(os.Getenv("GOOGLE_PLAY_INTEGRITY_DECRYPTION_KEY")),
		GooglePlayIntegrityVerificationKey: strings.TrimSpace(os.Getenv("GOOGLE_PLAY_INTEGRITY_VERIFICATION_KEY")),
	}, nil
}
