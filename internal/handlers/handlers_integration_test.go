package handlers

import (
	"encoding/json"
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
	"github.com/the-financial-workspace/backend/internal/models"
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

func TestListBudgets_ReturnsOwnedBudgets(t *testing.T) {
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222","user_id":"11111111-1111-1111-1111-111111111111","name":"Test Budget","monthly_income":5000000,"currency":"COP","billing_period_months":1,"billing_cutoff_day":1,"mode":"manual","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Get("/api/budgets", middleware.Protected(), ListBudgets)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	var budgets []models.Budget
	if err := json.Unmarshal(body, &budgets); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if len(budgets) != 1 {
		t.Errorf("expected 1 budget, got %d", len(budgets))
	}
	if budgets[0].Name != "Test Budget" {
		t.Errorf("name = %q, want %q", budgets[0].Name, "Test Budget")
	}
}

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

func TestCreateBudget_Success(t *testing.T) {
	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				// Budget count check returns empty.
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))
				return
			}
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(`[]`))
				return
			}
		})
	})

	app.Post("/api/budgets", middleware.Protected(), CreateBudget)

	payload := `{"name":"My Budget","monthly_income":5000000,"currency":"COP","billing_period_months":1,"billing_cutoff_day":1,"mode":"manual"}`
	req := httptest.NewRequest(http.MethodPost, "/api/budgets", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201, body: %s", resp.StatusCode, string(body))
	}
}

func TestCreateBudget_ValidationErrors(t *testing.T) {
	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Post("/api/budgets", middleware.Protected(), CreateBudget)

	tests := []struct {
		name    string
		payload string
		wantMsg string
	}{
		{"empty name", `{"name":"","monthly_income":5000000}`, "name is required"},
		{"name too long", `{"name":"` + strings.Repeat("a", 201) + `","monthly_income":5000000}`, "name too long"},
		{"zero income", `{"name":"Test","monthly_income":0}`, "monthly_income must be positive"},
		{"negative income", `{"name":"Test","monthly_income":-100}`, "monthly_income must be positive"},
		{"income too large", `{"name":"Test","monthly_income":2e15}`, "monthly_income exceeds maximum"},
		{"invalid mode", `{"name":"Test","monthly_income":5000000,"mode":"invalid"}`, "invalid mode"},
		{"invalid currency", `{"name":"Test","monthly_income":5000000,"currency":"ABCD"}`, "invalid currency"},
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

func TestCreateBudget_BudgetLimitReached(t *testing.T) {
	// Return 7 existing budgets.
	existingBudgets := make([]map[string]string, 7)
	for i := range existingBudgets {
		existingBudgets[i] = map[string]string{"id": uuid.New().String()}
	}
	budgetsJSON, _ := json.Marshal(existingBudgets)

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write(budgetsJSON)
		})
	})

	app.Post("/api/budgets", middleware.Protected(), CreateBudget)

	payload := `{"name":"Too Many","monthly_income":5000000}`
	req := httptest.NewRequest(http.MethodPost, "/api/budgets", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestCreateBudget_GuidedMode(t *testing.T) {
	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`[]`))
		})
	})

	app.Post("/api/budgets", middleware.Protected(), CreateBudget)

	payload := `{"name":"Guided Budget","monthly_income":5000000,"mode":"balanced"}`
	req := httptest.NewRequest(http.MethodPost, "/api/budgets", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 201, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== GetBudget ====================

func TestGetBudget_Success(t *testing.T) {
	budgetID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222","user_id":"11111111-1111-1111-1111-111111111111","name":"Test","monthly_income":5000000,"currency":"COP","billing_period_months":1,"billing_cutoff_day":1,"mode":"manual","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Get("/api/budgets/:id", middleware.Protected(), GetBudget)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/"+budgetID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}

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

func TestUpdateBudget_Success(t *testing.T) {
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222","user_id":"11111111-1111-1111-1111-111111111111","name":"Old Name","monthly_income":5000000,"currency":"COP","billing_period_months":1,"billing_cutoff_day":1,"mode":"manual","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPatch {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[]`))
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
	})

	app.Put("/api/budgets/:id", middleware.Protected(), UpdateBudget)

	payload := `{"name":"New Name"}`
	req := httptest.NewRequest(http.MethodPut, "/api/budgets/22222222-2222-2222-2222-222222222222", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}

func TestUpdateBudget_ValidationErrors(t *testing.T) {
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222","user_id":"11111111-1111-1111-1111-111111111111","name":"Test","monthly_income":5000000,"currency":"COP","billing_period_months":1,"billing_cutoff_day":1,"mode":"manual","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
	})

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

func TestDeleteBudget_Success(t *testing.T) {
	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			// GET for ownership verification.
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[{"id":"22222222-2222-2222-2222-222222222222"}]`))
		})
		mux.HandleFunc("/rest/v1/budget_expenses", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`[{"id":"sec-1"}]`))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("/rest/v1/budget_invites", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})

	app.Delete("/api/budgets/:id", middleware.Protected(), DeleteBudget)

	req := httptest.NewRequest(http.MethodDelete, "/api/budgets/22222222-2222-2222-2222-222222222222", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 204, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== Me endpoint ====================

func TestMe_Success(t *testing.T) {
	profileJSON := `[{"id":"11111111-1111-1111-1111-111111111111","email":"test@example.com","full_name":"Test User","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/profiles", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(profileJSON))
		})
	})

	app.Get("/api/auth/me", middleware.Protected(), Me)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}

func TestMe_ProfileNotFound(t *testing.T) {
	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/profiles", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Get("/api/auth/me", middleware.Protected(), Me)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// ==================== ListExpenses ====================

func TestListExpenses_Success(t *testing.T) {
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222"}]`
	expensesJSON := `[{"id":"33333333-3333-3333-3333-333333333333","budget_id":"22222222-2222-2222-2222-222222222222","subcategory_id":"44444444-4444-4444-4444-444444444444","amount":100,"description":"Test","expense_date":"2026-04-10","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/budget_expenses", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(expensesJSON))
		})
	})

	app.Get("/api/budgets/:id/expenses", middleware.Protected(), ListExpenses)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/22222222-2222-2222-2222-222222222222/expenses", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== CreateExpense ====================

func TestCreateExpense_Success(t *testing.T) {
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222"}]`
	subcatJSON := `[{"id":"44444444-4444-4444-4444-444444444444","category_id":"55555555-5555-5555-5555-555555555555"}]`
	sectionJSON := `[{"id":"55555555-5555-5555-5555-555555555555"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(subcatJSON))
		})
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(sectionJSON))
		})
		mux.HandleFunc("/rest/v1/budget_expenses", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`[]`))
		})
	})

	app.Post("/api/budgets/:id/expenses", middleware.Protected(), CreateExpense)

	payload := `{"category_id":"44444444-4444-4444-4444-444444444444","amount":150.50,"description":"Groceries","expense_date":"2026-04-10"}`
	req := httptest.NewRequest(http.MethodPost, "/api/budgets/22222222-2222-2222-2222-222222222222/expenses", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 201, body: %s", resp.StatusCode, string(body))
	}
}

func TestCreateExpense_ValidationErrors(t *testing.T) {
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

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
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("%s: status = %d, want 400, body: %s", tc.name, resp.StatusCode, string(body))
			}
		})
	}
}

// ==================== ListSections ====================

func TestListSections_Success(t *testing.T) {
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222"}]`
	sectionsJSON := `[{"id":"55555555-5555-5555-5555-555555555555","budget_id":"22222222-2222-2222-2222-222222222222","name":"Necesidades","allocation_percent":50,"icon":"home","sort_order":1,"created_at":"2026-01-01T00:00:00Z"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(sectionsJSON))
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Get("/api/budgets/:id/sections", middleware.Protected(), ListSections)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/22222222-2222-2222-2222-222222222222/sections", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}

// ==================== GetBudgetSummary ====================

func TestGetBudgetSummary_Success(t *testing.T) {
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222","user_id":"11111111-1111-1111-1111-111111111111","name":"Test","monthly_income":5000000,"currency":"COP","billing_period_months":1,"billing_cutoff_day":1,"mode":"manual","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}]`
	sectionsJSON := `[{"id":"55555555-5555-5555-5555-555555555555","budget_id":"22222222-2222-2222-2222-222222222222","name":"Necesidades","allocation_percent":100,"icon":"home","sort_order":1,"created_at":"2026-01-01T00:00:00Z"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(sectionsJSON))
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/budget_expenses", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Get("/api/budgets/:id/summary", middleware.Protected(), GetBudgetSummary)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/22222222-2222-2222-2222-222222222222/summary", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	var summary models.BudgetSummary
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if summary.TotalBudget != 5000000 {
		t.Errorf("TotalBudget = %v, want 5000000", summary.TotalBudget)
	}
}

// ==================== GetBudgetTrends ====================

func TestGetBudgetTrends_Success(t *testing.T) {
	budgetJSON := `[{"id":"22222222-2222-2222-2222-222222222222"}]`
	sectionsJSON := `[{"id":"55555555-5555-5555-5555-555555555555","budget_id":"22222222-2222-2222-2222-222222222222","name":"Necesidades","allocation_percent":100,"icon":"home","sort_order":1,"created_at":"2026-01-01T00:00:00Z"}]`

	app, token := setupTestEnv(t, func(mux *http.ServeMux) {
		mux.HandleFunc("/rest/v1/budgets", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(budgetJSON))
		})
		mux.HandleFunc("/rest/v1/budget_collaborators", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/budget_categories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(sectionsJSON))
		})
		mux.HandleFunc("/rest/v1/budget_subcategories", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
		mux.HandleFunc("/rest/v1/budget_expenses", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`[]`))
		})
	})

	app.Get("/api/budgets/:id/trends", middleware.Protected(), GetBudgetTrends)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/22222222-2222-2222-2222-222222222222/trends", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}
}
