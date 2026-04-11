package config

import (
	"os"
	"strings"
	"testing"
)

// setRequiredEnvVars sets all required environment variables with valid defaults
// for testing. Returns a cleanup function that restores the original values.
func setRequiredEnvVars(t *testing.T) func() {
	t.Helper()

	vars := map[string]string{
		"SUPABASE_URL":              "https://test.supabase.co",
		"SUPABASE_SERVICE_ROLE_KEY": "test-service-role-key",
		"JWT_SECRET":                "this-is-a-test-jwt-secret-that-is-at-least-32-characters-long",
		"GOOGLE_CLIENT_ID":          "test-google-client-id",
		"GOOGLE_CLIENT_SECRET":      "test-google-client-secret",
	}

	originals := make(map[string]string)
	for k := range vars {
		originals[k] = os.Getenv(k)
	}
	// Also save optional vars that might be set.
	for _, k := range []string{"FRONTEND_URL", "PORT", "CORS_ORIGIN"} {
		originals[k] = os.Getenv(k)
	}

	for k, v := range vars {
		os.Setenv(k, v)
	}
	// Clear optional vars to test defaults.
	os.Unsetenv("FRONTEND_URL")
	os.Unsetenv("PORT")
	os.Unsetenv("CORS_ORIGIN")

	return func() {
		for k, v := range originals {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}
}

// ==================== Load — Happy Path ====================

func TestLoad_AllRequiredVarsSet(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}

	if cfg.SupabaseURL != "https://test.supabase.co" {
		t.Errorf("SupabaseURL = %q, want %q", cfg.SupabaseURL, "https://test.supabase.co")
	}
	if cfg.SupabaseServiceRoleKey != "test-service-role-key" {
		t.Errorf("SupabaseServiceRoleKey = %q, want %q", cfg.SupabaseServiceRoleKey, "test-service-role-key")
	}
	if cfg.JWTSecret != "this-is-a-test-jwt-secret-that-is-at-least-32-characters-long" {
		t.Errorf("JWTSecret incorrect")
	}
	if cfg.GoogleClientID != "test-google-client-id" {
		t.Errorf("GoogleClientID = %q, want %q", cfg.GoogleClientID, "test-google-client-id")
	}
	if cfg.GoogleClientSecret != "test-google-client-secret" {
		t.Errorf("GoogleClientSecret = %q, want %q", cfg.GoogleClientSecret, "test-google-client-secret")
	}
}

// ==================== Load — Defaults ====================

func TestLoad_DefaultFrontendURL(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("FRONTEND_URL")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.FrontendURL != "http://localhost:3000" {
		t.Errorf("FrontendURL = %q, want %q", cfg.FrontendURL, "http://localhost:3000")
	}
}

func TestLoad_CustomFrontendURL(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("FRONTEND_URL", "https://app.example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.FrontendURL != "https://app.example.com" {
		t.Errorf("FrontendURL = %q, want %q", cfg.FrontendURL, "https://app.example.com")
	}
}

func TestLoad_DefaultPort(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("PORT")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
}

func TestLoad_CustomPort(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("PORT", "3000")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.Port != 3000 {
		t.Errorf("Port = %d, want 3000", cfg.Port)
	}
}

func TestLoad_DefaultCORSOrigin(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("CORS_ORIGIN")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.CORSOrigin != "http://localhost:3000" {
		t.Errorf("CORSOrigin = %q, want %q", cfg.CORSOrigin, "http://localhost:3000")
	}
}

func TestLoad_CustomCORSOrigin(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("CORS_ORIGIN", "https://app.example.com")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.CORSOrigin != "https://app.example.com" {
		t.Errorf("CORSOrigin = %q, want %q", cfg.CORSOrigin, "https://app.example.com")
	}
}

// ==================== Load — Missing Required Vars ====================

func TestLoad_MissingSupabaseURL(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("SUPABASE_URL")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail when SUPABASE_URL is missing")
	}
	if !strings.Contains(err.Error(), "SUPABASE_URL") {
		t.Errorf("error should mention SUPABASE_URL, got: %v", err)
	}
}

func TestLoad_MissingServiceRoleKey(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("SUPABASE_SERVICE_ROLE_KEY")
	os.Unsetenv("SUPABASE_ANON_KEY")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail when SUPABASE_SERVICE_ROLE_KEY is missing")
	}
	if !strings.Contains(err.Error(), "SUPABASE_SERVICE_ROLE_KEY") {
		t.Errorf("error should mention SUPABASE_SERVICE_ROLE_KEY, got: %v", err)
	}
}

func TestLoad_FallbackToAnonKey(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("SUPABASE_SERVICE_ROLE_KEY")
	os.Setenv("SUPABASE_ANON_KEY", "legacy-anon-key")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should succeed with SUPABASE_ANON_KEY fallback: %v", err)
	}
	if cfg.SupabaseServiceRoleKey != "legacy-anon-key" {
		t.Errorf("SupabaseServiceRoleKey = %q, want %q", cfg.SupabaseServiceRoleKey, "legacy-anon-key")
	}
}

func TestLoad_MissingJWTSecret(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("JWT_SECRET")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail when JWT_SECRET is missing")
	}
	if !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Errorf("error should mention JWT_SECRET, got: %v", err)
	}
}

func TestLoad_JWTSecretTooShort(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("JWT_SECRET", "short-secret") // less than 32 chars
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail when JWT_SECRET is too short")
	}
	if !strings.Contains(err.Error(), "32 characters") {
		t.Errorf("error should mention 32 characters, got: %v", err)
	}
}

func TestLoad_JWTSecretExactly32Chars(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("JWT_SECRET", strings.Repeat("x", 32))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should accept 32-char JWT_SECRET: %v", err)
	}
	if len(cfg.JWTSecret) != 32 {
		t.Errorf("JWTSecret length = %d, want 32", len(cfg.JWTSecret))
	}
}

func TestLoad_MissingGoogleClientID(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("GOOGLE_CLIENT_ID")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail when GOOGLE_CLIENT_ID is missing")
	}
	if !strings.Contains(err.Error(), "GOOGLE_CLIENT_ID") {
		t.Errorf("error should mention GOOGLE_CLIENT_ID, got: %v", err)
	}
}

func TestLoad_MissingGoogleClientSecret(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("GOOGLE_CLIENT_SECRET")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail when GOOGLE_CLIENT_SECRET is missing")
	}
	if !strings.Contains(err.Error(), "GOOGLE_CLIENT_SECRET") {
		t.Errorf("error should mention GOOGLE_CLIENT_SECRET, got: %v", err)
	}
}

// ==================== Load — Port Validation ====================

func TestLoad_InvalidPortFormat(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("PORT", "not-a-number")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail with invalid PORT")
	}
	if !strings.Contains(err.Error(), "PORT") {
		t.Errorf("error should mention PORT, got: %v", err)
	}
}

func TestLoad_PortZero(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("PORT", "0")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail with PORT=0")
	}
	if !strings.Contains(err.Error(), "PORT") {
		t.Errorf("error should mention PORT, got: %v", err)
	}
}

func TestLoad_PortNegative(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("PORT", "-1")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail with negative PORT")
	}
}

func TestLoad_PortTooHigh(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("PORT", "70000")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail with PORT > 65535")
	}
	if !strings.Contains(err.Error(), "65535") {
		t.Errorf("error should mention 65535, got: %v", err)
	}
}

func TestLoad_PortBoundary1(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("PORT", "1")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should accept PORT=1: %v", err)
	}
	if cfg.Port != 1 {
		t.Errorf("Port = %d, want 1", cfg.Port)
	}
}

func TestLoad_PortBoundary65535(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("PORT", "65535")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() should accept PORT=65535: %v", err)
	}
	if cfg.Port != 65535 {
		t.Errorf("Port = %d, want 65535", cfg.Port)
	}
}
