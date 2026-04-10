package middleware

import (
	"crypto/rsa"
	"crypto/rand"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"encoding/json"
)

// ==================== JWTSecret ====================

func TestJWTSecret_ReturnsConfiguredValue(t *testing.T) {
	Init("test-secret-value-12345")
	secret := JWTSecret()
	if string(secret) != "test-secret-value-12345" {
		t.Errorf("JWTSecret() = %q, want %q", string(secret), "test-secret-value-12345")
	}
}

func TestJWTSecret_EmptyAfterEmptyInit(t *testing.T) {
	Init("")
	secret := JWTSecret()
	if len(secret) != 0 {
		t.Errorf("JWTSecret() should be empty after Init(\"\"), got %q", string(secret))
	}
}

// ==================== GenerateToken ====================

func TestGenerateToken_ProducesValidToken(t *testing.T) {
	Init("my-test-secret")
	userID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	email := "test@example.com"

	tokenStr, err := GenerateToken(userID, email)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	if tokenStr == "" {
		t.Fatal("GenerateToken returned empty string")
	}

	// Parse the token back and verify claims.
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil {
		t.Fatalf("failed to parse generated token: %v", err)
	}
	if !token.Valid {
		t.Error("generated token should be valid")
	}
	if claims.UserID != userID.String() {
		t.Errorf("UserID claim = %q, want %q", claims.UserID, userID.String())
	}
	if claims.Email != email {
		t.Errorf("Email claim = %q, want %q", claims.Email, email)
	}
	if claims.Issuer != "financial-workspace" {
		t.Errorf("Issuer = %q, want %q", claims.Issuer, "financial-workspace")
	}
	if claims.Subject != userID.String() {
		t.Errorf("Subject = %q, want %q", claims.Subject, userID.String())
	}
}

// ==================== Protected Middleware — Token Signing Method ====================

// helper to create a Fiber app with the Protected middleware and a test handler.
func setupProtectedApp() *fiber.App {
	app := fiber.New()
	app.Get("/protected", Protected(), func(c *fiber.Ctx) error {
		uid := GetUserID(c)
		return c.JSON(fiber.Map{"user_id": uid.String()})
	})
	return app
}

func TestProtected_RejectsTokenWithWrongSigningMethod_RSA(t *testing.T) {
	Init("test-secret-for-rsa-check")

	// Generate an RSA key and sign a token with RS256 instead of HS256.
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	claims := &Claims{
		UserID: uuid.New().String(),
		Email:  "evil@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenStr, err := token.SignedString(rsaKey)
	if err != nil {
		t.Fatalf("failed to sign RSA token: %v", err)
	}

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("RSA-signed token should be rejected, got status %d", resp.StatusCode)
	}
}

// ==================== Protected Middleware — Expired Token ====================

func TestProtected_RejectsExpiredToken(t *testing.T) {
	Init("test-secret-expired")

	claims := &Claims{
		UserID: uuid.New().String(),
		Email:  "expired@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)), // expired
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expired token should be rejected, got status %d", resp.StatusCode)
	}
}

// ==================== Protected Middleware — Future NotBefore ====================

func TestProtected_RejectsTokenWithFutureNBF(t *testing.T) {
	Init("test-secret-nbf")

	claims := &Claims{
		UserID: uuid.New().String(),
		Email:  "future@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(2 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)), // not yet valid
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("token with future nbf should be rejected, got status %d", resp.StatusCode)
	}
}

// ==================== Protected Middleware — Missing Required Claims ====================

func TestProtected_RejectsTokenWithoutUserID(t *testing.T) {
	Init("test-secret-no-userid")

	claims := &Claims{
		UserID: "", // empty user_id
		Email:  "no-uid@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	// Empty UserID will fail uuid.Parse => 401
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("token without user_id should be rejected, got status %d", resp.StatusCode)
	}
}

func TestProtected_RejectsTokenWithInvalidUUID(t *testing.T) {
	Init("test-secret-invalid-uuid")

	claims := &Claims{
		UserID: "not-a-uuid",
		Email:  "bad-uuid@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("token with invalid UUID should be rejected, got status %d", resp.StatusCode)
	}
}

// ==================== Protected Middleware — Wrong Issuer ====================

func TestProtected_RejectsTokenWithWrongIssuer(t *testing.T) {
	Init("test-secret-wrong-issuer")

	claims := &Claims{
		UserID: uuid.New().String(),
		Email:  "wrong-issuer@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "some-other-service", // wrong issuer
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, err := token.SignedString(jwtSecret)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("token with wrong issuer should be rejected, got status %d", resp.StatusCode)
	}
}

// ==================== Protected Middleware — Malformed Tokens ====================

func TestProtected_RejectsMalformedTokens(t *testing.T) {
	Init("test-secret-malformed")

	malformed := []struct {
		name  string
		token string
	}{
		{"empty token", ""},
		{"random garbage", "asdkjf9283jrklsdjf"},
		{"base64 only", "eyJhbGciOiJIUzI1NiJ9"},
		{"two parts only", "eyJhbGciOiJIUzI1NiJ9.eyJ0ZXN0IjoidmFsdWUifQ"},
		{"three dots", "a.b.c"},
	}

	app := setupProtectedApp()

	for _, tc := range malformed {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Header.Set("Authorization", "Bearer "+tc.token)

			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("app.Test failed: %v", err)
			}
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("malformed token %q should be rejected, got status %d", tc.name, resp.StatusCode)
			}
		})
	}
}

// ==================== Protected Middleware — Authorization Header Parsing ====================

func TestProtected_MissingAuthorizationHeader(t *testing.T) {
	Init("test-secret-noheader")

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	// No Authorization header set.

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing auth header should return 401, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]string
	if err := json.Unmarshal(body, &result); err == nil {
		if result["error"] != "missing authorization header" {
			t.Errorf("error message = %q, want %q", result["error"], "missing authorization header")
		}
	}
}

func TestProtected_MissingBearerPrefix(t *testing.T) {
	Init("test-secret-nobearer")

	userID := uuid.New()
	tokenStr, _ := GenerateToken(userID, "user@example.com")

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", tokenStr) // no "Bearer " prefix

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing Bearer prefix should return 401, got %d", resp.StatusCode)
	}
}

func TestProtected_EmptyBearerToken(t *testing.T) {
	Init("test-secret-emptybearer")

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer ")

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("empty bearer token should return 401, got %d", resp.StatusCode)
	}
}

func TestProtected_WrongScheme(t *testing.T) {
	Init("test-secret-wrongscheme")

	userID := uuid.New()
	tokenStr, _ := GenerateToken(userID, "user@example.com")

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Basic "+tokenStr)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("Basic scheme should return 401, got %d", resp.StatusCode)
	}
}

// ==================== Protected Middleware — Valid Token ====================

func TestProtected_AcceptsValidToken(t *testing.T) {
	Init("test-secret-valid")

	userID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	tokenStr, err := GenerateToken(userID, "valid@example.com")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("valid token should return 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if result["user_id"] != userID.String() {
		t.Errorf("user_id = %q, want %q", result["user_id"], userID.String())
	}
}

func TestProtected_BearerCaseInsensitive(t *testing.T) {
	Init("test-secret-case")

	userID := uuid.New()
	tokenStr, _ := GenerateToken(userID, "case@example.com")

	app := setupProtectedApp()

	// Test "bearer" lowercase
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "bearer "+tokenStr)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("lowercase 'bearer' should be accepted, got status %d", resp.StatusCode)
	}

	// Test "BEARER" uppercase
	req2 := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req2.Header.Set("Authorization", "BEARER "+tokenStr)
	resp2, _ := app.Test(req2)
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("uppercase 'BEARER' should be accepted, got status %d", resp2.StatusCode)
	}
}

// ==================== Protected Middleware — Token Signed with Wrong Secret ====================

func TestProtected_RejectsTokenSignedWithWrongSecret(t *testing.T) {
	Init("correct-secret")

	// Sign token with a different secret.
	claims := &Claims{
		UserID: uuid.New().String(),
		Email:  "wrong-key@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte("wrong-secret"))

	app := setupProtectedApp()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tokenStr)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("token signed with wrong secret should be rejected, got status %d", resp.StatusCode)
	}
}

// ==================== GetUserID ====================

func TestGetUserID_ReturnsNilForUnauthenticatedContext(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		uid := GetUserID(c)
		if uid != uuid.Nil {
			return c.SendString("FAIL")
		}
		return c.SendString("OK")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, _ := app.Test(req)
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "OK" {
		t.Error("GetUserID should return uuid.Nil for unauthenticated context")
	}
}
