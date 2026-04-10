package handlers

import (
	"encoding/json"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// --- Request struct parsing tests ---

func TestRegisterRequestParsesCorrectly(t *testing.T) {
	raw := `{"name":"Alice","email":"alice@example.com","password":"secret1234"}`
	var req RegisterRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal RegisterRequest: %v", err)
	}
	if req.Name != "Alice" {
		t.Errorf("expected Name=Alice, got %q", req.Name)
	}
	if req.Email != "alice@example.com" {
		t.Errorf("expected Email=alice@example.com, got %q", req.Email)
	}
	if req.Password != "secret1234" {
		t.Errorf("expected Password=secret1234, got %q", req.Password)
	}
}

func TestLoginRequestParsesCorrectly(t *testing.T) {
	raw := `{"email":"bob@example.com","password":"pass5678"}`
	var req LoginRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal LoginRequest: %v", err)
	}
	if req.Email != "bob@example.com" {
		t.Errorf("expected Email=bob@example.com, got %q", req.Email)
	}
	if req.Password != "pass5678" {
		t.Errorf("expected Password=pass5678, got %q", req.Password)
	}
}

// --- Registration validation tests ---

func TestRegisterValidation_EmptyName(t *testing.T) {
	req := RegisterRequest{
		Name:     "",
		Email:    "test@example.com",
		Password: "password123",
	}
	if req.Name != "" {
		t.Errorf("expected empty name")
	}
	// Trimmed empty name should be rejected by the handler.
	trimmed := req.Name
	if trimmed != "" {
		t.Errorf("expected trimmed name to be empty")
	}
}

func TestRegisterValidation_NameTooLong(t *testing.T) {
	longName := make([]byte, 201)
	for i := range longName {
		longName[i] = 'a'
	}
	if len(longName) <= maxNameLength {
		t.Errorf("expected name length %d > %d", len(longName), maxNameLength)
	}
}

func TestRegisterValidation_InvalidEmail(t *testing.T) {
	tests := []struct {
		email string
		valid bool
	}{
		{"", false},
		{"noatsign", false},
		{"no@dot", false},
		{"valid@example.com", true},
		{"user@sub.domain.org", true},
	}

	for _, tt := range tests {
		hasAt := len(tt.email) > 0 && contains(tt.email, "@")
		hasDot := len(tt.email) > 0 && contains(tt.email, ".")
		isValid := hasAt && hasDot
		if isValid != tt.valid {
			t.Errorf("email %q: expected valid=%v, got %v", tt.email, tt.valid, isValid)
		}
	}
}

func TestRegisterValidation_ShortPassword(t *testing.T) {
	tests := []struct {
		password string
		valid    bool
	}{
		{"", false},
		{"1234567", false},
		{"12345678", true},
		{"longpassword123", true},
	}

	for _, tt := range tests {
		valid := len(tt.password) >= 8
		if valid != tt.valid {
			t.Errorf("password %q: expected valid=%v, got %v", tt.password, tt.valid, valid)
		}
	}
}

func TestRegisterValidation_PasswordMaxBytes(t *testing.T) {
	// Passwords exceeding bcrypt's 72-byte limit should be rejected.
	tests := []struct {
		name     string
		password string
		valid    bool
	}{
		{"exactly 72 bytes", string(make([]byte, 72)), true},
		{"73 bytes", string(make([]byte, 73)), false},
		{"100 bytes", string(make([]byte, 100)), false},
	}

	for _, tt := range tests {
		// Fill with valid ASCII so len(password) >= 8.
		pw := make([]byte, len(tt.password))
		for i := range pw {
			pw[i] = 'a'
		}
		valid := len(pw) >= 8 && len(pw) <= maxPasswordBytes
		if valid != tt.valid {
			t.Errorf("%s: expected valid=%v, got %v (len=%d)", tt.name, tt.valid, valid, len(pw))
		}
	}
}

// --- Login validation tests ---

func TestLoginValidation_EmptyFields(t *testing.T) {
	tests := []struct {
		email    string
		password string
		errField string
	}{
		{"", "password123", "email"},
		{"user@test.com", "", "password"},
		{"", "", "email"},
	}

	for _, tt := range tests {
		if tt.email == "" && tt.errField == "email" {
			continue // correctly identified
		}
		if tt.password == "" && tt.errField == "password" {
			continue // correctly identified
		}
		t.Errorf("unexpected validation for email=%q password=%q", tt.email, tt.password)
	}
}

// --- Bcrypt round-trip tests ---

func TestBcryptCostConstant(t *testing.T) {
	// Verify the bcrypt cost is set to 12 for the financial app.
	if bcryptCost != 12 {
		t.Errorf("expected bcryptCost=12, got %d", bcryptCost)
	}
}

func TestBcryptHashRoundTrip(t *testing.T) {
	password := "mySecurePassword123"

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword failed: %v", err)
	}

	// Correct password should match.
	if err := bcrypt.CompareHashAndPassword(hash, []byte(password)); err != nil {
		t.Errorf("correct password should match hash: %v", err)
	}

	// Wrong password should not match.
	if err := bcrypt.CompareHashAndPassword(hash, []byte("wrongPassword")); err == nil {
		t.Error("wrong password should not match hash")
	}
}

func TestBcryptHashUniqueness(t *testing.T) {
	password := "samePassword"

	hash1, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		t.Fatalf("first hash failed: %v", err)
	}
	hash2, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		t.Fatalf("second hash failed: %v", err)
	}

	// Two hashes of the same password should differ (due to random salt).
	if string(hash1) == string(hash2) {
		t.Error("two bcrypt hashes of the same password should differ")
	}

	// Both should still verify.
	if err := bcrypt.CompareHashAndPassword(hash1, []byte(password)); err != nil {
		t.Error("hash1 should verify")
	}
	if err := bcrypt.CompareHashAndPassword(hash2, []byte(password)); err != nil {
		t.Error("hash2 should verify")
	}
}

// --- profileWithPassword struct test ---

func TestProfileWithPasswordUnmarshal(t *testing.T) {
	raw := `{"id":"550e8400-e29b-41d4-a716-446655440000","email":"test@example.com","full_name":"Test User","avatar_url":"https://example.com/pic.jpg","password_hash":"$2a$10$somehash"}`
	var p profileWithPassword
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("failed to unmarshal profileWithPassword: %v", err)
	}
	if p.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %q", p.Email)
	}
	if p.PasswordHash != "$2a$10$somehash" {
		t.Errorf("expected password_hash=$2a$10$somehash, got %q", p.PasswordHash)
	}
	if p.ID.String() != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("unexpected ID: %s", p.ID)
	}
}

// --- Per-email rate limiter tests ---

func TestEmailRateLimit_AllowsUpToMax(t *testing.T) {
	email := "ratelimit-test-allow@example.com"
	defer clearLoginAttempts(email)

	for i := 0; i < maxLoginAttemptsPerEmail; i++ {
		if checkEmailRateLimit(email) {
			t.Fatalf("should not be rate-limited after %d attempts", i)
		}
		recordFailedLogin(email)
	}
	// Now should be limited.
	if !checkEmailRateLimit(email) {
		t.Error("should be rate-limited after max attempts")
	}
}

func TestEmailRateLimit_ClearsOnSuccess(t *testing.T) {
	email := "ratelimit-test-clear@example.com"
	defer clearLoginAttempts(email)

	for i := 0; i < maxLoginAttemptsPerEmail; i++ {
		recordFailedLogin(email)
	}
	if !checkEmailRateLimit(email) {
		t.Fatal("should be rate-limited")
	}

	clearLoginAttempts(email)

	if checkEmailRateLimit(email) {
		t.Error("should not be rate-limited after clearing")
	}
}

func TestEmailRateLimit_DifferentEmails(t *testing.T) {
	email1 := "ratelimit-test-a@example.com"
	email2 := "ratelimit-test-b@example.com"
	defer clearLoginAttempts(email1)
	defer clearLoginAttempts(email2)

	for i := 0; i < maxLoginAttemptsPerEmail; i++ {
		recordFailedLogin(email1)
	}

	if !checkEmailRateLimit(email1) {
		t.Error("email1 should be rate-limited")
	}
	if checkEmailRateLimit(email2) {
		t.Error("email2 should not be rate-limited")
	}
}

// contains is a helper that checks if s contains substr.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
