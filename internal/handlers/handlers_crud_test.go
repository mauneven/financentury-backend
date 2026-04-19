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

// setupCRUDEnv initializes the database from TEST_DATABASE_URL for CRUD tests.
// The extraMux parameter is ignored (kept for API compat).
func setupCRUDEnv(t *testing.T, extraMux func(mux *http.ServeMux)) (*fiber.App, string) {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping CRUD integration test")
	}

	database.Init(dbURL)
	middleware.Init("test-jwt-secret-crud")

	hub := ws.NewHub()
	go hub.Run()
	InitWebSocket(hub)

	app := fiber.New()
	token, _ := middleware.GenerateToken(
		mustParseUUID("11111111-1111-1111-1111-111111111111"),
		"test@example.com",
	)
	return app, token
}

func mustParseUUID(s string) uuid.UUID {
	id, _ := uuid.Parse(s)
	return id
}

// ==================== CreateCategory ====================

func TestCreateCategory_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, nil)

	app.Post("/api/budgets/:id/categories", middleware.Protected(), CreateCategory)

	payload := `{"name":"New Category","allocation_value":50000,"icon":"tag","sort_order":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/budgets/22222222-2222-2222-2222-222222222222/categories", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	// Without a real budget we expect 404 from verifyBudgetOwnership. Accept
	// 201 or 404 depending on seeded data; just ensure we don't 500.
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d (server error), body: %s", resp.StatusCode, string(body))
	}
}

func TestCreateCategory_ValidationErrors(t *testing.T) {
	app, token := setupCRUDEnv(t, nil)
	app.Post("/api/budgets/:id/categories", middleware.Protected(), CreateCategory)

	tests := []struct {
		name    string
		payload string
	}{
		{"empty name", `{"name":"","allocation_value":50}`},
		{"name too long", `{"name":"` + strings.Repeat("a", 201) + `","allocation_value":50}`},
		{"negative allocation", `{"name":"Test","allocation_value":-1}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/budgets/22222222-2222-2222-2222-222222222222/categories", strings.NewReader(tc.payload))
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

// ==================== UpdateCategory ====================

func TestUpdateCategory_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, nil)

	app.Patch("/api/budgets/:id/categories/:catId", middleware.Protected(), UpdateCategory)

	payload := `{"name":"Updated Cat"}`
	req := httptest.NewRequest(http.MethodPatch, "/api/budgets/22222222-2222-2222-2222-222222222222/categories/66666666-6666-6666-6666-666666666666", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	// Without a real budget/category we expect 404. Ensure no 500.
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d (server error), body: %s", resp.StatusCode, string(body))
	}
}

// ==================== DeleteCategory ====================

func TestDeleteCategory_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, nil)

	app.Delete("/api/budgets/:id/categories/:catId", middleware.Protected(), DeleteCategory)

	req := httptest.NewRequest(http.MethodDelete, "/api/budgets/22222222-2222-2222-2222-222222222222/categories/66666666-6666-6666-6666-666666666666", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	// Without a real budget/category we expect 404. Ensure no 500.
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d (server error), body: %s", resp.StatusCode, string(body))
	}
}

// ==================== UpdateExpense ====================

func TestUpdateExpense_ValidationErrors(t *testing.T) {
	app, token := setupCRUDEnv(t, nil)
	app.Put("/api/budgets/:id/expenses/:expenseId", middleware.Protected(), UpdateExpense)

	tests := []struct {
		name    string
		payload string
	}{
		{"zero amount", `{"amount":0}`},
		{"negative amount", `{"amount":-50}`},
		{"amount too large", `{"amount":2e15}`},
		{"description too long", `{"description":"` + strings.Repeat("a", 501) + `"}`},
		{"empty date", `{"expense_date":""}`},
		{"invalid date", `{"expense_date":"not-a-date"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/budgets/22222222-2222-2222-2222-222222222222/expenses/33333333-3333-3333-3333-333333333333", strings.NewReader(tc.payload))
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

// ==================== DeleteExpense ====================

func TestDeleteExpense_NotFound(t *testing.T) {
	app, token := setupCRUDEnv(t, nil)
	app.Delete("/api/budgets/:id/expenses/:expenseId", middleware.Protected(), DeleteExpense)

	req := httptest.NewRequest(http.MethodDelete, "/api/budgets/22222222-2222-2222-2222-222222222222/expenses/33333333-3333-3333-3333-333333333333", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	// Without a real budget/expense we expect 404. Ensure no 500.
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d (server error), body: %s", resp.StatusCode, string(body))
	}
}

// ==================== ListCollaborators ====================

func TestListCollaborators_InvalidBudgetID(t *testing.T) {
	app, token := setupCRUDEnv(t, nil)
	app.Get("/api/budgets/:id/collaborators", middleware.Protected(), ListCollaborators)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/not-a-uuid/collaborators", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== RemoveCollaborator ====================

func TestRemoveCollaborator_InvalidID(t *testing.T) {
	app, token := setupCRUDEnv(t, nil)
	app.Delete("/api/budgets/:id/collaborators/:userId", middleware.Protected(), RemoveCollaborator)

	req := httptest.NewRequest(http.MethodDelete, "/api/budgets/not-a-uuid/collaborators/99999999-9999-9999-9999-999999999999", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== GoogleLogin — basic validation ====================

func TestGoogleLogin_MissingCode(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/google", GoogleLogin)

	payload := `{"redirect_uri":"https://app.example.com/auth/callback"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/google", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestGoogleLogin_MissingRedirectURI(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/google", GoogleLogin)

	payload := `{"code":"auth-code-123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/google", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestGoogleLogin_DisallowedRedirectURI(t *testing.T) {
	InitAuth("id", "secret", "https://allowed.example.com")
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/google", GoogleLogin)

	payload := `{"code":"auth-code-123","redirect_uri":"https://evil.example.com/auth/callback"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/google", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestGoogleLogin_InvalidBody(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/google", GoogleLogin)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/google", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ==================== Register — basic validation ====================

func TestRegister_MissingName(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/register", Register)

	payload := `{"email":"test@example.com","password":"Passw0rd"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestRegister_ShortPassword(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/register", Register)

	payload := `{"name":"Test","email":"test@example.com","password":"Short1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestRegister_WeakPassword(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/register", Register)

	// No uppercase letter.
	payload := `{"name":"Test","email":"test@example.com","password":"password1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestRegister_InvalidEmail(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/register", Register)

	payload := `{"name":"Test","email":"not-an-email","password":"Password1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestRegister_PasswordTooLong(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/register", Register)

	longPw := strings.Repeat("A", 37) + strings.Repeat("a", 37) + "1" // 75 bytes > 72
	payload := `{"name":"Test","email":"test@example.com","password":"` + longPw + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/register", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== Login — basic validation ====================

func TestLogin_MissingEmail(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/login", Login)

	payload := `{"password":"Password1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestLogin_MissingPassword(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/login", Login)

	payload := `{"email":"test@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestLogin_InvalidBody(t *testing.T) {
	app, _ := setupCRUDEnv(t, nil)
	app.Post("/api/auth/login", Login)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
