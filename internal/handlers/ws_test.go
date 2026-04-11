package handlers

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// ==================== parseWSToken ====================

func TestParseWSToken_ValidToken(t *testing.T) {
	middleware.Init("test-ws-secret-for-parsing")

	userID := uuid.New()
	tokenStr, err := middleware.GenerateToken(userID, "user@example.com")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	parsedUserID, err := parseWSToken(tokenStr)
	if err != nil {
		t.Fatalf("parseWSToken failed: %v", err)
	}

	if parsedUserID != userID.String() {
		t.Errorf("parsedUserID = %q, want %q", parsedUserID, userID.String())
	}
}

func TestParseWSToken_ExpiredToken(t *testing.T) {
	middleware.Init("test-ws-secret-expired")

	claims := &middleware.Claims{
		UserID: uuid.New().String(),
		Email:  "expired@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(middleware.JWTSecret())

	_, err := parseWSToken(tokenStr)
	if err == nil {
		t.Error("expired token should be rejected")
	}
}

func TestParseWSToken_WrongSigningMethod(t *testing.T) {
	middleware.Init("test-ws-secret-wrong-method")

	// Create a token with none signing method (a security attack vector).
	claims := &middleware.Claims{
		UserID: uuid.New().String(),
		Email:  "attacker@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tokenStr, _ := token.SignedString(jwt.UnsafeAllowNoneSignatureType)

	_, err := parseWSToken(tokenStr)
	if err == nil {
		t.Error("token with 'none' signing method should be rejected")
	}
}

func TestParseWSToken_WrongSecret(t *testing.T) {
	middleware.Init("correct-ws-secret")

	claims := &middleware.Claims{
		UserID: uuid.New().String(),
		Email:  "user@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString([]byte("wrong-secret"))

	_, err := parseWSToken(tokenStr)
	if err == nil {
		t.Error("token signed with wrong secret should be rejected")
	}
}

func TestParseWSToken_WrongIssuer(t *testing.T) {
	middleware.Init("test-ws-secret-wrong-issuer")

	claims := &middleware.Claims{
		UserID: uuid.New().String(),
		Email:  "user@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "other-service",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(middleware.JWTSecret())

	_, err := parseWSToken(tokenStr)
	if err == nil {
		t.Error("token with wrong issuer should be rejected")
	}
}

func TestParseWSToken_EmptyUserID(t *testing.T) {
	middleware.Init("test-ws-secret-empty-uid")

	claims := &middleware.Claims{
		UserID: "",
		Email:  "user@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(middleware.JWTSecret())

	_, err := parseWSToken(tokenStr)
	if err == nil {
		t.Error("token with empty user_id should be rejected")
	}
}

func TestParseWSToken_WhitespaceOnlyUserID(t *testing.T) {
	middleware.Init("test-ws-secret-whitespace-uid")

	claims := &middleware.Claims{
		UserID: "   \t\n  ",
		Email:  "user@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "financial-workspace",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenStr, _ := token.SignedString(middleware.JWTSecret())

	_, err := parseWSToken(tokenStr)
	if err == nil {
		t.Error("token with whitespace-only user_id should be rejected")
	}
}

func TestParseWSToken_MalformedToken(t *testing.T) {
	middleware.Init("test-ws-secret-malformed")

	malformed := []string{
		"",
		"not-a-token",
		"a.b.c",
		"eyJhbGciOiJIUzI1NiJ9",
	}

	for _, tokenStr := range malformed {
		_, err := parseWSToken(tokenStr)
		if err == nil {
			t.Errorf("malformed token %q should be rejected", tokenStr)
		}
	}
}

// ==================== InitWebSocket / GetHub ====================

func TestInitWebSocket_SetsHub(t *testing.T) {
	hub := ws.NewHub()
	InitWebSocket(hub)

	got := GetHub()
	if got != hub {
		t.Error("GetHub() should return the hub set by InitWebSocket")
	}
}

func TestGetHub_NilBeforeInit(t *testing.T) {
	// Reset the global hub to nil.
	wsHub = nil
	got := GetHub()
	if got != nil {
		t.Error("GetHub() should return nil before InitWebSocket is called")
	}
}

// ==================== broadcast helper ====================

func TestBroadcast_NilHub_NoPanic(t *testing.T) {
	// Set hub to nil and ensure broadcast does not panic.
	wsHub = nil
	broadcast("budget-1", "test_event", map[string]string{"test": "data"})
	// If we reach here without panic, the test passes.
}

func TestBroadcast_WithHub_SendsMessage(t *testing.T) {
	hub := ws.NewHub()
	go hub.Run()
	InitWebSocket(hub)

	// Calling broadcast should not panic even with no connected clients.
	broadcast("budget-1", ws.MessageTypeBudgetCreated, map[string]string{"name": "Test"})
	// Give the hub a moment to process.
	time.Sleep(50 * time.Millisecond)
}
