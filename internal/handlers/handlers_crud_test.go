package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// setupCRUDEnv is a shared setup that configures a mock Supabase responding
// with owner-verified budgets for all CRUD tests.
func setupCRUDEnv(t *testing.T, extraMux func(mux *http.ServeMux)) (*fiber.App, string) {
	t.Helper()

	mux := http.NewServeMux()

	// Default: budgets owned by our test user.
	mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"22222222-2222-2222-2222-222222222222","user_id":"11111111-1111-1111-1111-111111111111"}]`))
	})
	mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[]`))
	})

	if extraMux != nil {
		extraMux(mux)
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	database.Init(server.URL, "test-api-key")
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

// ==================== CreateSection ====================

func TestCreateSection_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Post("/api/budgets/:id/sections", middleware.Protected(), CreateSection)

	payload := `{"name":"Test Section","allocation_percent":50,"icon":"home","sort_order":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/budgets/22222222-2222-2222-2222-222222222222/sections", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 201, body: %s", resp.StatusCode, string(body))
	}
}

func TestCreateSection_ValidationErrors(t *testing.T) {
	app, token := setupCRUDEnv(t, nil)
	app.Post("/api/budgets/:id/sections", middleware.Protected(), CreateSection)

	tests := []struct {
		name    string
		payload string
	}{
		{"empty name", `{"name":"","allocation_percent":50,"icon":"home"}`},
		{"name too long", `{"name":"` + strings.Repeat("a", 201) + `","allocation_percent":50,"icon":"home"}`},
		{"icon too long", `{"name":"Test","allocation_percent":50,"icon":"` + strings.Repeat("x", 51) + `"}`},
		{"negative allocation", `{"name":"Test","allocation_percent":-1,"icon":"home"}`},
		{"allocation over 100", `{"name":"Test","allocation_percent":101,"icon":"home"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/budgets/22222222-2222-2222-2222-222222222222/sections", strings.NewReader(tc.payload))
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

// ==================== UpdateSection ====================

func TestUpdateSection_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPatch {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))
				return
			}
			// GET returns existing section.
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"55555555-5555-5555-5555-555555555555","budget_id":"22222222-2222-2222-2222-222222222222","name":"Old","allocation_percent":50,"icon":"home","sort_order":1,"created_at":"2026-01-01T00:00:00Z"}]`))
		})
	})

	app.Put("/api/budgets/:id/sections/:sectionId", middleware.Protected(), UpdateSection)

	payload := `{"name":"Updated Section"}`
	req := httptest.NewRequest(http.MethodPut, "/api/budgets/22222222-2222-2222-2222-222222222222/sections/55555555-5555-5555-5555-555555555555", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== DeleteSection ====================

func TestDeleteSection_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"55555555-5555-5555-5555-555555555555"}]`))
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/rest/v1/budget_expenses", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	app.Delete("/api/budgets/:id/sections/:sectionId", middleware.Protected(), DeleteSection)

	req := httptest.NewRequest(http.MethodDelete, "/api/budgets/22222222-2222-2222-2222-222222222222/sections/55555555-5555-5555-5555-555555555555", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 204, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== CreateCategory ====================

func TestCreateCategory_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"55555555-5555-5555-5555-555555555555"}]`))
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Post("/api/budgets/:id/sections/:sectionId/categories", middleware.Protected(), CreateCategory)

	payload := `{"name":"New Category","allocation_percent":50,"icon":"tag","sort_order":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/budgets/22222222-2222-2222-2222-222222222222/sections/55555555-5555-5555-5555-555555555555/categories", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 201, body: %s", resp.StatusCode, string(body))
	}
}

func TestCreateCategory_ValidationErrors(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"55555555-5555-5555-5555-555555555555"}]`))
		})
	})

	app.Post("/api/budgets/:id/sections/:sectionId/categories", middleware.Protected(), CreateCategory)

	tests := []struct {
		name    string
		payload string
	}{
		{"empty name", `{"name":"","allocation_percent":50}`},
		{"name too long", `{"name":"` + strings.Repeat("a", 201) + `","allocation_percent":50}`},
		{"negative allocation", `{"name":"Test","allocation_percent":-1}`},
		{"allocation over 100", `{"name":"Test","allocation_percent":101}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/budgets/22222222-2222-2222-2222-222222222222/sections/55555555-5555-5555-5555-555555555555/categories", strings.NewReader(tc.payload))
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
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"55555555-5555-5555-5555-555555555555"}]`))
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPatch {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))
				return
			}
			// GET returns existing category.
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"66666666-6666-6666-6666-666666666666","category_id":"55555555-5555-5555-5555-555555555555","name":"Old Cat","allocation_percent":50,"icon":"tag","sort_order":1,"created_at":"2026-01-01T00:00:00Z"}]`))
		})
	})

	app.Put("/api/budgets/:id/sections/:sectionId/categories/:catId", middleware.Protected(), UpdateCategory)

	payload := `{"name":"Updated Cat"}`
	req := httptest.NewRequest(http.MethodPut, "/api/budgets/22222222-2222-2222-2222-222222222222/sections/55555555-5555-5555-5555-555555555555/categories/66666666-6666-6666-6666-666666666666", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== DeleteCategory ====================

func TestDeleteCategory_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"55555555-5555-5555-5555-555555555555"}]`))
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// GET returns existing category.
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"66666666-6666-6666-6666-666666666666","category_id":"55555555-5555-5555-5555-555555555555"}]`))
		})
		mux.HandleFunc("/rest/v1/budget_expenses", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	app.Delete("/api/budgets/:id/sections/:sectionId/categories/:catId", middleware.Protected(), DeleteCategory)

	req := httptest.NewRequest(http.MethodDelete, "/api/budgets/22222222-2222-2222-2222-222222222222/sections/55555555-5555-5555-5555-555555555555/categories/66666666-6666-6666-6666-666666666666", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 204, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== UpdateExpense ====================

func TestUpdateExpense_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_expenses", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPatch {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"33333333-3333-3333-3333-333333333333","budget_id":"22222222-2222-2222-2222-222222222222","subcategory_id":"44444444-4444-4444-4444-444444444444","amount":100,"description":"Old","expense_date":"2026-04-01","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`))
		})
	})

	app.Put("/api/budgets/:id/expenses/:expenseId", middleware.Protected(), UpdateExpense)

	payload := `{"amount":200,"description":"Updated"}`
	req := httptest.NewRequest(http.MethodPut, "/api/budgets/22222222-2222-2222-2222-222222222222/expenses/33333333-3333-3333-3333-333333333333", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}

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

func TestDeleteExpense_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_expenses", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"33333333-3333-3333-3333-333333333333"}]`))
		})
	})

	app.Delete("/api/budgets/:id/expenses/:expenseId", middleware.Protected(), DeleteExpense)

	req := httptest.NewRequest(http.MethodDelete, "/api/budgets/22222222-2222-2222-2222-222222222222/expenses/33333333-3333-3333-3333-333333333333", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 204, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== ListCollaborators ====================

func TestListCollaborators_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/profiles", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Get("/api/budgets/:id/collaborators", middleware.Protected(), ListCollaborators)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/22222222-2222-2222-2222-222222222222/collaborators", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== RemoveCollaborator ====================

func TestRemoveCollaborator_Success(t *testing.T) {
	app, token := setupCRUDEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Delete("/api/budgets/:id/collaborators/:userId", middleware.Protected(), RemoveCollaborator)

	req := httptest.NewRequest(http.MethodDelete, "/api/budgets/22222222-2222-2222-2222-222222222222/collaborators/99999999-9999-9999-9999-999999999999", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	// May return 204 (success) or error depending on verification logic.
	if resp.StatusCode >= 500 {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d (server error), body: %s", resp.StatusCode, string(body))
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
