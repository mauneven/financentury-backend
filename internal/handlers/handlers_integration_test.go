package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// setupTestEnv initializes the database client from TEST_DATABASE_URL, sets up
// middleware, and returns a Fiber app ready for integration testing.
// The setupMux parameter is ignored (kept for API compat) — tests run against
// a real PostgreSQL database.
func setupTestEnv(t *testing.T, setupMux func(mux *http.ServeMux)) (*fiber.App, string) {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}

	database.Init(dbURL)
	middleware.Init("test-jwt-secret-for-handlers")

	// Set up WebSocket hub for broadcast calls.
	hub := ws.NewHub()
	go hub.Run()
	InitWebSocket(hub)

	app := fiber.New()

	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	token, _ := middleware.GenerateToken(userID, "test@example.com")

	return app, token
}

// ==================== ListBudgets ====================

func TestListBudgets_Unauthorized(t *testing.T) {
	app, _ := setupTestEnv(t, func(mux *http.ServeMux) {})
	app.Get("/api/budgets", ListBudgets) // No Protected middleware

	req := httptest.NewRequest(http.MethodGet, "/api/budgets", nil)
	resp, _ := app.Test(req)

	// Without Protected middleware, requireUserID returns false.
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ==================== CreateBudget ====================

func TestCreateBudget_ValidationErrors(t *testing.T) {
	app, token := setupTestEnv(t, nil)

	app.Post("/api/budgets", middleware.Protected(), CreateBudget)

	tests := []struct {
		name    string
		payload string
	}{
		{"empty name", `{"name":"","monthly_income":5000000}`},
		{"name too long", `{"name":"` + strings.Repeat("a", 201) + `","monthly_income":5000000}`},
		{"zero income", `{"name":"Test","monthly_income":0}`},
		{"negative income", `{"name":"Test","monthly_income":-100}`},
		{"income too large", `{"name":"Test","monthly_income":2e15}`},
		{"invalid mode", `{"name":"Test","monthly_income":5000000,"mode":"invalid"}`},
		{"invalid currency", `{"name":"Test","monthly_income":5000000,"currency":"ABCD"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/budgets", strings.NewReader(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, _ := app.Test(req)
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("%s: status = %d, want 400, body: %s", tc.name, resp.StatusCode, string(body))
			}
		})
	}
}

// ==================== GetBudget ====================

func TestGetBudget_InvalidID(t *testing.T) {
	app, token := setupTestEnv(t, func(mux *http.ServeMux) {})
	app.Get("/api/budgets/:id", middleware.Protected(), GetBudget)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ==================== UpdateBudget ====================

func TestUpdateBudget_ValidationErrors(t *testing.T) {
	app, token := setupTestEnv(t, nil)

	app.Put("/api/budgets/:id", middleware.Protected(), UpdateBudget)

	tests := []struct {
		name    string
		payload string
	}{
		{"empty name", `{"name":""}`},
		{"name too long", `{"name":"` + strings.Repeat("a", 201) + `"}`},
		{"zero income", `{"monthly_income":0}`},
		{"negative income", `{"monthly_income":-100}`},
		{"income too large", `{"monthly_income":2e15}`},
		{"invalid mode", `{"mode":"invalid"}`},
		{"invalid currency", `{"currency":"ABCD"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/budgets/22222222-2222-2222-2222-222222222222", strings.NewReader(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, _ := app.Test(req)
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("%s: status = %d, want 400, body: %s", tc.name, resp.StatusCode, string(body))
			}
		})
	}
}

// ==================== DeleteBudget ====================

func TestDeleteBudget_InvalidID(t *testing.T) {
	app, token := setupTestEnv(t, nil)
	app.Delete("/api/budgets/:id", middleware.Protected(), DeleteBudget)

	req := httptest.NewRequest(http.MethodDelete, "/api/budgets/not-a-uuid", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== CreateExpense ====================

func TestCreateExpense_ValidationErrors(t *testing.T) {
	app, token := setupTestEnv(t, nil)

	app.Post("/api/budgets/:id/expenses", middleware.Protected(), CreateExpense)

	tests := []struct {
		name    string
		payload string
	}{
		{"missing category_id", `{"amount":100}`},
		{"zero amount", `{"category_id":"44444444-4444-4444-4444-444444444444","amount":0}`},
		{"negative amount", `{"category_id":"44444444-4444-4444-4444-444444444444","amount":-50}`},
		{"amount too large", `{"category_id":"44444444-4444-4444-4444-444444444444","amount":2e15}`},
		{"description too long", `{"category_id":"44444444-4444-4444-4444-444444444444","amount":100,"description":"` + strings.Repeat("a", 501) + `"}`},
		{"invalid date", `{"category_id":"44444444-4444-4444-4444-444444444444","amount":100,"expense_date":"not-a-date"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/budgets/22222222-2222-2222-2222-222222222222/expenses", strings.NewReader(tc.payload))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, _ := app.Test(req)
			// Validation failures should short-circuit with 400 before DB calls.
			// Some cases require budget access verification; those may return 404.
			if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("%s: status = %d, want 400 or 404, body: %s", tc.name, resp.StatusCode, string(body))
			}
		})
	}
}

// ==================== Me endpoint ====================

func TestMe_Unauthorized(t *testing.T) {
	app, _ := setupTestEnv(t, nil)
	app.Get("/api/auth/me", Me)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ==================== ListExpenses ====================

func TestListExpenses_InvalidBudgetID(t *testing.T) {
	app, token := setupTestEnv(t, nil)
	app.Get("/api/budgets/:id/expenses", middleware.Protected(), ListExpenses)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/not-a-uuid/expenses", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== GetBudgetSummary ====================

func TestGetBudgetSummary_InvalidBudgetID(t *testing.T) {
	app, token := setupTestEnv(t, nil)
	app.Get("/api/budgets/:id/summary", middleware.Protected(), GetBudgetSummary)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/not-a-uuid/summary", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== GetBudgetTrends ====================

func TestGetBudgetTrends_InvalidBudgetID(t *testing.T) {
	app, token := setupTestEnv(t, nil)
	app.Get("/api/budgets/:id/trends", middleware.Protected(), GetBudgetTrends)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/not-a-uuid/trends", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== ListCategories ====================

func TestListCategories_InvalidBudgetID(t *testing.T) {
	app, token := setupTestEnv(t, nil)
	app.Get("/api/budgets/:id/categories", middleware.Protected(), ListCategories)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/not-a-uuid/categories", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}
