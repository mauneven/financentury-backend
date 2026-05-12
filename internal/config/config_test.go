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
		"DATABASE_URL":         "postgresql://user:pass@localhost:5432/testdb?sslmode=disable",
		"JWT_SECRET":           "this-is-a-test-jwt-secret-that-is-at-least-32-characters-long",
		"GOOGLE_CLIENT_ID":     "test-google-client-id",
		"GOOGLE_CLIENT_SECRET": "test-google-client-secret",
	}

	originals := make(map[string]string)
	for k := range vars {
		originals[k] = os.Getenv(k)
	}
	for _, k := range []string{"FRONTEND_URL", "PORT", "CORS_ORIGIN"} {
		originals[k] = os.Getenv(k)
	}

	for k, v := range vars {
		os.Setenv(k, v)
	}
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

	if cfg.DatabaseURL != "postgresql://user:pass@localhost:5432/testdb?sslmode=disable" {
		t.Errorf("DatabaseURL = %q, want the test connection string", cfg.DatabaseURL)
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
	if cfg.Port != 3000 {
		t.Errorf("Port = %d, want 3000", cfg.Port)
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

func TestLoad_MissingDatabaseURL(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Unsetenv("DATABASE_URL")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() should fail when DATABASE_URL is missing")
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("error should mention DATABASE_URL, got: %v", err)
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

	os.Setenv("JWT_SECRET", "short-secret")
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

// ==================== Load — Bot Protection (captcha) =====================

// TestLoad_CaptchaVarsOptional verifies that every captcha-related env
// var defaults to "" — the backend must start without bot protection
// configured (no-op verifiers + startup warning, per the spec).
func TestLoad_CaptchaVarsOptional(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	for _, k := range []string{
		"TURNSTILE_SECRET",
		"APPLE_APP_ATTEST_TEAM_ID",
		"APPLE_APP_ATTEST_BUNDLE",
		"GOOGLE_PLAY_INTEGRITY_PACKAGE_NAME",
		"GOOGLE_PLAY_INTEGRITY_DECRYPTION_KEY",
		"GOOGLE_PLAY_INTEGRITY_VERIFICATION_KEY",
	} {
		os.Unsetenv(k)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed without captcha vars: %v", err)
	}
	if cfg.TurnstileSecret != "" ||
		cfg.AppleAppAttestTeamID != "" ||
		cfg.AppleAppAttestBundleID != "" ||
		cfg.GooglePlayIntegrityPackageName != "" ||
		cfg.GooglePlayIntegrityDecryptionKey != "" ||
		cfg.GooglePlayIntegrityVerificationKey != "" {
		t.Errorf("unset captcha vars should produce empty strings, got %+v", cfg)
	}
}

// TestLoad_CaptchaVarsParsed verifies that all six captcha vars are read
// off the environment exactly as the spec documents.
func TestLoad_CaptchaVarsParsed(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	vars := map[string]string{
		"TURNSTILE_SECRET":                       "0x4Asecret",
		"APPLE_APP_ATTEST_TEAM_ID":               "ABCD123456",
		"APPLE_APP_ATTEST_BUNDLE":                "com.test.app",
		"GOOGLE_PLAY_INTEGRITY_PACKAGE_NAME":     "com.test.android",
		"GOOGLE_PLAY_INTEGRITY_DECRYPTION_KEY":   "ZGVj",
		"GOOGLE_PLAY_INTEGRITY_VERIFICATION_KEY": "dmVy",
	}
	for k, v := range vars {
		os.Setenv(k, v)
		defer os.Unsetenv(k)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.TurnstileSecret != "0x4Asecret" {
		t.Errorf("TurnstileSecret = %q", cfg.TurnstileSecret)
	}
	if cfg.AppleAppAttestTeamID != "ABCD123456" {
		t.Errorf("AppleAppAttestTeamID = %q", cfg.AppleAppAttestTeamID)
	}
	if cfg.AppleAppAttestBundleID != "com.test.app" {
		t.Errorf("AppleAppAttestBundleID = %q", cfg.AppleAppAttestBundleID)
	}
	if cfg.GooglePlayIntegrityPackageName != "com.test.android" {
		t.Errorf("GooglePlayIntegrityPackageName = %q", cfg.GooglePlayIntegrityPackageName)
	}
	if cfg.GooglePlayIntegrityDecryptionKey != "ZGVj" {
		t.Errorf("GooglePlayIntegrityDecryptionKey = %q", cfg.GooglePlayIntegrityDecryptionKey)
	}
	if cfg.GooglePlayIntegrityVerificationKey != "dmVy" {
		t.Errorf("GooglePlayIntegrityVerificationKey = %q", cfg.GooglePlayIntegrityVerificationKey)
	}
}

// TestLoad_CaptchaVarsTrimmed verifies the whitespace-tolerance of env
// var parsing — operators sometimes wrap secrets in quotes or paste with
// trailing newlines.
func TestLoad_CaptchaVarsTrimmed(t *testing.T) {
	cleanup := setRequiredEnvVars(t)
	defer cleanup()

	os.Setenv("TURNSTILE_SECRET", "  the-secret  \n")
	defer os.Unsetenv("TURNSTILE_SECRET")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() failed: %v", err)
	}
	if cfg.TurnstileSecret != "the-secret" {
		t.Errorf("TurnstileSecret = %q, want trimmed", cfg.TurnstileSecret)
	}
}
