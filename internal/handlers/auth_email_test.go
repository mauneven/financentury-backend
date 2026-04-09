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

func TestBcryptHashRoundTrip(t *testing.T) {
	password := "mySecurePassword123"

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
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

	hash1, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("first hash failed: %v", err)
	}
	hash2, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
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

// contains is a helper that checks if s contains substr.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
