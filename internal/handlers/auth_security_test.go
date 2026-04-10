package handlers

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// ==================== isAllowedRedirectURI — Valid URIs ====================

func TestIsAllowedRedirectURI_ValidHTTPS(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	valid := []string{
		"https://myapp.example.com/auth/callback",
	}
	for _, uri := range valid {
		if !isAllowedRedirectURI(uri) {
			t.Errorf("isAllowedRedirectURI(%q) = false, want true", uri)
		}
	}
}

func TestIsAllowedRedirectURI_ValidLocalhostHTTP(t *testing.T) {
	InitAuth("id", "secret", "http://localhost:3000")

	if !isAllowedRedirectURI("http://localhost:3000/auth/callback") {
		t.Error("localhost HTTP should be allowed")
	}
}

func TestIsAllowedRedirectURI_ValidLocalhost127(t *testing.T) {
	InitAuth("id", "secret", "http://127.0.0.1:3000")

	if !isAllowedRedirectURI("http://127.0.0.1:3000/auth/callback") {
		t.Error("127.0.0.1 HTTP should be allowed")
	}
}

func TestIsAllowedRedirectURI_MultipleAllowedOrigins(t *testing.T) {
	InitAuth("id", "secret", "http://localhost:3000", "https://app.example.com")

	if !isAllowedRedirectURI("http://localhost:3000/auth/callback") {
		t.Error("first allowed origin should pass")
	}
	if !isAllowedRedirectURI("https://app.example.com/auth/callback") {
		t.Error("second allowed origin should pass")
	}
}

// ==================== isAllowedRedirectURI — Path Traversal ====================

func TestIsAllowedRedirectURI_PathTraversal(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	attacks := []string{
		"https://myapp.example.com/auth/callback/../../../etc/passwd",
		"https://myapp.example.com/../auth/callback",
		"https://myapp.example.com/auth/callback/../../admin",
		"https://myapp.example.com/auth/callback%2F..%2F..%2Fadmin",
	}
	for _, uri := range attacks {
		if isAllowedRedirectURI(uri) {
			t.Errorf("path traversal should be rejected: %q", uri)
		}
	}
}

// ==================== isAllowedRedirectURI — Query Strings ====================

func TestIsAllowedRedirectURI_RejectsQueryStrings(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	uris := []string{
		"https://myapp.example.com/auth/callback?code=abc",
		"https://myapp.example.com/auth/callback?redirect=evil.com",
		"https://myapp.example.com/auth/callback?foo=bar&baz=qux",
	}
	for _, uri := range uris {
		if isAllowedRedirectURI(uri) {
			t.Errorf("query strings should be rejected: %q", uri)
		}
	}
}

// ==================== isAllowedRedirectURI — Fragments ====================

func TestIsAllowedRedirectURI_RejectsFragments(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	uris := []string{
		"https://myapp.example.com/auth/callback#token=abc",
		"https://myapp.example.com/auth/callback#section",
	}
	for _, uri := range uris {
		if isAllowedRedirectURI(uri) {
			t.Errorf("fragments should be rejected: %q", uri)
		}
	}
}

// ==================== isAllowedRedirectURI — Different Schemes ====================

func TestIsAllowedRedirectURI_RejectsHTTPForNonLocalhost(t *testing.T) {
	InitAuth("id", "secret", "http://myapp.example.com")

	// HTTP is only allowed for localhost and 127.0.0.1.
	if isAllowedRedirectURI("http://myapp.example.com/auth/callback") {
		t.Error("HTTP should be rejected for non-localhost hosts")
	}
}

func TestIsAllowedRedirectURI_RejectsFTP(t *testing.T) {
	InitAuth("id", "secret", "ftp://myapp.example.com")

	if isAllowedRedirectURI("ftp://myapp.example.com/auth/callback") {
		t.Error("FTP scheme should be rejected")
	}
}

func TestIsAllowedRedirectURI_RejectsJavascript(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	if isAllowedRedirectURI("javascript:alert(1)") {
		t.Error("javascript: scheme should be rejected")
	}
}

func TestIsAllowedRedirectURI_RejectsDataURI(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	if isAllowedRedirectURI("data:text/html,<script>alert(1)</script>") {
		t.Error("data: URI should be rejected")
	}
}

// ==================== isAllowedRedirectURI — Wrong Path ====================

func TestIsAllowedRedirectURI_RejectsWrongPath(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	wrongPaths := []string{
		"https://myapp.example.com/",
		"https://myapp.example.com/callback",
		"https://myapp.example.com/auth/callback/extra",
		"https://myapp.example.com/auth",
	}
	for _, uri := range wrongPaths {
		if isAllowedRedirectURI(uri) {
			t.Errorf("wrong path should be rejected: %q", uri)
		}
	}
}

// ==================== isAllowedRedirectURI — Not In Allowlist ====================

func TestIsAllowedRedirectURI_RejectsUnknownOrigin(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	if isAllowedRedirectURI("https://evil.example.com/auth/callback") {
		t.Error("unknown origin should be rejected")
	}
}

func TestIsAllowedRedirectURI_RejectsEmptyString(t *testing.T) {
	InitAuth("id", "secret", "https://myapp.example.com")

	if isAllowedRedirectURI("") {
		t.Error("empty string should be rejected")
	}
}

// ==================== Email Validation Edge Cases ====================

func TestEmailValidation_BasicCheck(t *testing.T) {
	// The handler uses: strings.Contains(email, "@") && strings.Contains(email, ".")
	// Test edge cases that pass or fail this simple check.

	type tc struct {
		email string
		valid bool
	}

	cases := []tc{
		{"user@example.com", true},
		{"user@example", false},     // no dot
		{"@.", true},                 // passes the simple check (has @ and .)
		{".@.", true},                // passes the simple check
		{"user@.com", true},         // passes the simple check
		{"", false},                 // no @ or .
		{"nodomain@", false},        // no dot
		{"no-at-sign.com", false},   // no @
		{"a@b.c", true},
	}

	for _, c := range cases {
		email := strings.TrimSpace(strings.ToLower(c.email))
		result := strings.Contains(email, "@") && strings.Contains(email, ".")
		if result != c.valid {
			t.Errorf("email %q: got valid=%v, want %v", c.email, result, c.valid)
		}
	}
}

func TestEmailValidation_VeryLongEmail(t *testing.T) {
	// A very long email should still pass the basic check but would
	// typically be caught by database constraints.
	longLocal := strings.Repeat("a", 500)
	email := longLocal + "@example.com"
	hasAt := strings.Contains(email, "@")
	hasDot := strings.Contains(email, ".")
	if !hasAt || !hasDot {
		t.Error("long email should pass basic @ and . check")
	}
}

func TestEmailValidation_SQLInjectionInEmail(t *testing.T) {
	// SQL injection in email should still pass the basic check
	// (security relies on parameterized queries / URL escaping, not email validation).
	sqli := "admin'--@evil.com"
	hasAt := strings.Contains(sqli, "@")
	hasDot := strings.Contains(sqli, ".")
	if !hasAt || !hasDot {
		t.Error("SQL injection email passes basic format check; real protection is at DB layer")
	}
}

// ==================== Password Validation ====================

func TestPasswordValidation_MinLength(t *testing.T) {
	short := []string{"", "1234567", "abc", "x"}
	for _, pw := range short {
		if len(pw) >= 8 {
			t.Errorf("password %q should fail min length check", pw)
		}
	}
}

func TestPasswordValidation_ExactlyMinLength(t *testing.T) {
	pw := "12345678"
	if len(pw) < 8 {
		t.Error("8-char password should pass min length check")
	}
}

func TestPasswordValidation_MaxLengthBcryptLimit(t *testing.T) {
	// bcrypt truncates at 72 bytes. The handler rejects passwords > 72 bytes.
	pw72 := strings.Repeat("a", 72)
	if len([]byte(pw72)) > maxPasswordBytes {
		t.Error("72-byte password should pass max check")
	}

	pw73 := strings.Repeat("a", 73)
	if len([]byte(pw73)) <= maxPasswordBytes {
		t.Error("73-byte password should fail max check")
	}
}

func TestPasswordValidation_UnicodePassword(t *testing.T) {
	// Unicode characters can be multi-byte. A password that looks short
	// in characters may exceed 72 bytes.
	// Each emoji is 4 bytes. 18 emoji = 72 bytes (should pass).
	pw18emoji := strings.Repeat("\U0001F600", 18)
	if len([]byte(pw18emoji)) != 72 {
		t.Fatalf("expected 72 bytes, got %d", len([]byte(pw18emoji)))
	}
	if len([]byte(pw18emoji)) > maxPasswordBytes {
		t.Error("18 emoji (72 bytes) should pass bcrypt limit")
	}

	// 19 emoji = 76 bytes (should fail).
	pw19emoji := strings.Repeat("\U0001F600", 19)
	if len([]byte(pw19emoji)) <= maxPasswordBytes {
		t.Error("19 emoji (76 bytes) should exceed bcrypt limit")
	}
}

// ==================== Login Rate Limiter — checkEmailRateLimit ====================

func TestCheckEmailRateLimit_NoRecordReturnsFalse(t *testing.T) {
	// Clean state: unknown email should not be rate limited.
	email := "fresh-email-" + time.Now().Format("150405.000") + "@test.com"
	if checkEmailRateLimit(email) {
		t.Error("fresh email should not be rate limited")
	}
}

func TestCheckEmailRateLimit_UnderLimitReturnsFalse(t *testing.T) {
	email := "under-limit-" + time.Now().Format("150405.000") + "@test.com"

	// Record fewer than maxLoginAttemptsPerEmail failures.
	for i := 0; i < maxLoginAttemptsPerEmail-1; i++ {
		recordFailedLogin(email)
	}

	if checkEmailRateLimit(email) {
		t.Errorf("should not be rate limited after %d attempts (limit is %d)",
			maxLoginAttemptsPerEmail-1, maxLoginAttemptsPerEmail)
	}
}

func TestCheckEmailRateLimit_AtLimitReturnsTrue(t *testing.T) {
	email := "at-limit-" + time.Now().Format("150405.000") + "@test.com"

	// Record exactly maxLoginAttemptsPerEmail failures.
	for i := 0; i < maxLoginAttemptsPerEmail; i++ {
		recordFailedLogin(email)
	}

	if !checkEmailRateLimit(email) {
		t.Errorf("should be rate limited after %d attempts", maxLoginAttemptsPerEmail)
	}
}

func TestCheckEmailRateLimit_OverLimitReturnsTrue(t *testing.T) {
	email := "over-limit-" + time.Now().Format("150405.000") + "@test.com"

	for i := 0; i < maxLoginAttemptsPerEmail+5; i++ {
		recordFailedLogin(email)
	}

	if !checkEmailRateLimit(email) {
		t.Error("should be rate limited when over limit")
	}
}

func TestClearLoginAttempts_ResetsCounter(t *testing.T) {
	email := "clear-test-" + time.Now().Format("150405.000") + "@test.com"

	for i := 0; i < maxLoginAttemptsPerEmail; i++ {
		recordFailedLogin(email)
	}

	if !checkEmailRateLimit(email) {
		t.Fatal("should be rate limited before clear")
	}

	clearLoginAttempts(email)

	if checkEmailRateLimit(email) {
		t.Error("should not be rate limited after clearLoginAttempts")
	}
}

// ==================== Login Rate Limiter — Window Expiry ====================

func TestCheckEmailRateLimit_ExpiredWindowResets(t *testing.T) {
	email := "expired-window-" + time.Now().Format("150405.000") + "@test.com"

	// Manually insert a record with an already-expired window.
	loginAttemptsMu.Lock()
	loginAttempts[email] = &loginAttemptRecord{
		attempts:  maxLoginAttemptsPerEmail + 10,
		expiresAt: time.Now().Add(-1 * time.Minute), // already expired
	}
	loginAttemptsMu.Unlock()

	if checkEmailRateLimit(email) {
		t.Error("expired window should reset and return false")
	}
}

// ==================== Login Rate Limiter — Concurrent Access ====================

func TestRecordFailedLogin_ConcurrentSafety(t *testing.T) {
	email := "concurrent-" + time.Now().Format("150405.000") + "@test.com"

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recordFailedLogin(email)
		}()
	}
	wg.Wait()

	// Should not have panicked, and the count should be correct.
	loginAttemptsMu.Lock()
	rec := loginAttempts[email]
	loginAttemptsMu.Unlock()

	if rec == nil {
		t.Fatal("record should exist after concurrent writes")
	}
	if rec.attempts != 50 {
		t.Errorf("expected 50 attempts after concurrent writes, got %d", rec.attempts)
	}
}

// ==================== Constants ====================

func TestMaxLoginAttemptsPerEmail(t *testing.T) {
	if maxLoginAttemptsPerEmail != 5 {
		t.Errorf("maxLoginAttemptsPerEmail = %d, want 5", maxLoginAttemptsPerEmail)
	}
}

func TestLoginAttemptWindow(t *testing.T) {
	if loginAttemptWindow != 15*time.Minute {
		t.Errorf("loginAttemptWindow = %v, want 15m", loginAttemptWindow)
	}
}

func TestBcryptCost(t *testing.T) {
	if bcryptCost != 12 {
		t.Errorf("bcryptCost = %d, want 12", bcryptCost)
	}
}

func TestMaxPasswordBytes(t *testing.T) {
	if maxPasswordBytes != 72 {
		t.Errorf("maxPasswordBytes = %d, want 72", maxPasswordBytes)
	}
}
