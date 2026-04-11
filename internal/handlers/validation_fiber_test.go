package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// ==================== Error Helpers via Fiber ====================

func TestErrUnauthorized(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return errUnauthorized(c)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result models.ErrorResponse
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if result.Error != "unauthorized" {
		t.Errorf("error = %q, want %q", result.Error, "unauthorized")
	}
}

func TestErrBadRequest(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return errBadRequest(c, "invalid input")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result models.ErrorResponse
	json.Unmarshal(body, &result)
	if result.Error != "invalid input" {
		t.Errorf("error = %q, want %q", result.Error, "invalid input")
	}
}

func TestErrNotFound(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return errNotFound(c, "budget not found")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result models.ErrorResponse
	json.Unmarshal(body, &result)
	if result.Error != "budget not found" {
		t.Errorf("error = %q, want %q", result.Error, "budget not found")
	}
}

func TestErrInternal(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return errInternal(c, "database error")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result models.ErrorResponse
	json.Unmarshal(body, &result)
	if result.Error != "database error" {
		t.Errorf("error = %q, want %q", result.Error, "database error")
	}
}

func TestErrForbidden(t *testing.T) {
	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return errForbidden(c, "not allowed")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	var result models.ErrorResponse
	json.Unmarshal(body, &result)
	if result.Error != "not allowed" {
		t.Errorf("error = %q, want %q", result.Error, "not allowed")
	}
}

// ==================== requireUserID ====================

func TestRequireUserID_WithValidToken(t *testing.T) {
	middleware.Init("test-secret-for-requireuserid")

	userID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	token, _ := middleware.GenerateToken(userID, "test@example.com")

	var gotUserID uuid.UUID
	var gotOK bool

	app := fiber.New()
	app.Get("/test", middleware.Protected(), func(c *fiber.Ctx) error {
		gotUserID, gotOK = requireUserID(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !gotOK {
		t.Error("requireUserID should return true for authenticated user")
	}
	if gotUserID != userID {
		t.Errorf("userID = %v, want %v", gotUserID, userID)
	}
}

func TestRequireUserID_WithoutAuth(t *testing.T) {
	var gotUserID uuid.UUID
	var gotOK bool

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		gotUserID, gotOK = requireUserID(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotOK {
		t.Error("requireUserID should return false for unauthenticated user")
	}
	if gotUserID != uuid.Nil {
		t.Errorf("userID should be nil, got %v", gotUserID)
	}
}

// ==================== parseUUIDParam ====================

func TestParseUUIDParam_ValidUUID(t *testing.T) {
	expectedID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	var gotID uuid.UUID
	var gotOK bool

	app := fiber.New()
	app.Get("/test/:id", func(c *fiber.Ctx) error {
		gotID, gotOK = parseUUIDParam(c, "id")
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test/"+expectedID.String(), nil)
	app.Test(req)

	if !gotOK {
		t.Error("parseUUIDParam should return true for valid UUID")
	}
	if gotID != expectedID {
		t.Errorf("id = %v, want %v", gotID, expectedID)
	}
}

func TestParseUUIDParam_InvalidUUID(t *testing.T) {
	var gotID uuid.UUID
	var gotOK bool

	app := fiber.New()
	app.Get("/test/:id", func(c *fiber.Ctx) error {
		gotID, gotOK = parseUUIDParam(c, "id")
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test/not-a-uuid", nil)
	app.Test(req)

	if gotOK {
		t.Error("parseUUIDParam should return false for invalid UUID")
	}
	if gotID != uuid.Nil {
		t.Errorf("id should be nil, got %v", gotID)
	}
}

func TestParseUUIDParam_EmptyParam(t *testing.T) {
	var gotOK bool

	app := fiber.New()
	app.Get("/test/:id", func(c *fiber.Ctx) error {
		_, gotOK = parseUUIDParam(c, "id")
		return c.SendString("ok")
	})

	// An empty string for :id (route won't match with empty, but test with a dash)
	req := httptest.NewRequest(http.MethodGet, "/test/-", nil)
	app.Test(req)

	if gotOK {
		t.Error("parseUUIDParam should return false for '-'")
	}
}

// ==================== parsePaginationParams ====================

func TestParsePaginationParams_Defaults(t *testing.T) {
	var gotLimit, gotOffset int

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		gotLimit, gotOffset = parsePaginationParams(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	app.Test(req)

	if gotLimit != 100 {
		t.Errorf("default limit = %d, want 100", gotLimit)
	}
	if gotOffset != 0 {
		t.Errorf("default offset = %d, want 0", gotOffset)
	}
}

func TestParsePaginationParams_CustomValues(t *testing.T) {
	var gotLimit, gotOffset int

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		gotLimit, gotOffset = parsePaginationParams(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test?limit=50&offset=20", nil)
	app.Test(req)

	if gotLimit != 50 {
		t.Errorf("limit = %d, want 50", gotLimit)
	}
	if gotOffset != 20 {
		t.Errorf("offset = %d, want 20", gotOffset)
	}
}

func TestParsePaginationParams_LimitCapped(t *testing.T) {
	var gotLimit int

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		gotLimit, _ = parsePaginationParams(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test?limit=1000", nil)
	app.Test(req)

	if gotLimit != 500 {
		t.Errorf("limit should be capped at 500, got %d", gotLimit)
	}
}

func TestParsePaginationParams_InvalidLimit(t *testing.T) {
	var gotLimit int

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		gotLimit, _ = parsePaginationParams(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test?limit=abc", nil)
	app.Test(req)

	if gotLimit != 100 {
		t.Errorf("invalid limit should default to 100, got %d", gotLimit)
	}
}

func TestParsePaginationParams_NegativeLimit(t *testing.T) {
	var gotLimit int

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		gotLimit, _ = parsePaginationParams(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test?limit=-5", nil)
	app.Test(req)

	if gotLimit != 100 {
		t.Errorf("negative limit should default to 100, got %d", gotLimit)
	}
}

func TestParsePaginationParams_ZeroLimit(t *testing.T) {
	var gotLimit int

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		gotLimit, _ = parsePaginationParams(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test?limit=0", nil)
	app.Test(req)

	if gotLimit != 100 {
		t.Errorf("zero limit should default to 100, got %d", gotLimit)
	}
}

func TestParsePaginationParams_NegativeOffset(t *testing.T) {
	var gotOffset int

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		_, gotOffset = parsePaginationParams(c)
		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test?offset=-5", nil)
	app.Test(req)

	if gotOffset != 0 {
		t.Errorf("negative offset should default to 0, got %d", gotOffset)
	}
}
