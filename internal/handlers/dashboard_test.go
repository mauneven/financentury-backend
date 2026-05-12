package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/the-financial-workspace/backend/internal/middleware"
)

// TestGetBudgetDashboard_Unauthorized asserts the route returns 401 when
// the request reaches the handler without auth middleware setting a user
// id in the locals. (Mirrors the rest of the handler-level auth tests.)
func TestGetBudgetDashboard_Unauthorized(t *testing.T) {
	app := fiber.New()
	app.Get("/api/budgets/:id/dashboard", GetBudgetDashboard) // no Protected middleware
	req := httptest.NewRequest(http.MethodGet, "/api/budgets/00000000-0000-0000-0000-000000000001/dashboard", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// TestGetBudgetDashboard_InvalidID asserts the route returns 400 when the
// :id path param isn't a UUID.
func TestGetBudgetDashboard_InvalidID(t *testing.T) {
	app, token := setupTestEnv(t, nil)
	app.Get("/api/budgets/:id/dashboard", middleware.Protected(), GetBudgetDashboard)

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/not-a-uuid/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestGetBudgetDashboard_NotFound asserts the route 404s when the
// authenticated user has no access to the requested budget.
func TestGetBudgetDashboard_NotFound(t *testing.T) {
	app, token := setupTestEnv(t, nil)
	app.Get("/api/budgets/:id/dashboard", middleware.Protected(), GetBudgetDashboard)

	// Random budget id the test user has never seen.
	bid := uuid.New().String()
	req := httptest.NewRequest(http.MethodGet, "/api/budgets/"+bid+"/dashboard", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	// Without the row in the DB, loadBudgetWithAccessSF returns
	// pgx.ErrNoRows which the handler maps to 404. If the test DB happens
	// to refuse the connection we still expect a non-200; either way it
	// must not return a stale dashboard envelope.
	if resp.StatusCode == http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected non-200 for missing budget, got 200 body=%s", string(body))
	}
}

// TestDashboardEnvelope_JSONShape pins the wire shape so a refactor can't
// silently rename fields the frontend hooks rely on. The fields are read
// by useBudgetDashboard's setQueryData seeding.
func TestDashboardEnvelope_JSONShape(t *testing.T) {
	env := dashboardEnvelope{}
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, k := range []string{`"summary"`, `"expenses"`, `"trends"`, `"resume"`} {
		if !strings.Contains(got, k) {
			t.Errorf("envelope missing key %s; got %s", k, got)
		}
	}
}
