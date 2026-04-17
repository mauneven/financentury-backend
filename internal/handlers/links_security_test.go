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

// setupLinkSecurityEnv initializes the database from TEST_DATABASE_URL for
// link security tests. Returns a Fiber app and a JWT token for the test user.
func setupLinkSecurityEnv(t *testing.T) (*fiber.App, string) {
	t.Helper()

	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping link security test")
	}

	database.Init(dbURL)
	middleware.Init("test-jwt-secret-link-security")

	hub := ws.NewHub()
	go hub.Run()
	InitWebSocket(hub)

	app := fiber.New()
	return app, ""
}

// tokenForUser generates a JWT for the given user ID and email.
func tokenForUser(userID uuid.UUID, email string) string {
	token, _ := middleware.GenerateToken(userID, email)
	return token
}

// Static UUIDs used across tests.
var (
	ownerUserID    = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	collabUserID   = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	thirdUserID    = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	budgetAID      = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	budgetBID      = uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	budgetCID      = uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	sectionA1ID    = uuid.MustParse("a1a1a1a1-a1a1-a1a1-a1a1-a1a1a1a1a1a1")
	sectionB1ID    = uuid.MustParse("b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1")
	categoryA1aID  = uuid.MustParse("a11a11a1-a11a-a11a-a11a-a11a11a11a11")
)

// seedLinkTestData inserts the minimal set of rows needed by most link
// security tests: three users, three budgets, sections, and a category.
// Budget A is owned by ownerUserID, budget B by collabUserID, budget C by
// thirdUserID. All use USD currency.
func seedLinkTestData(t *testing.T) {
	t.Helper()
	ctx := database.DB.Pool

	// Clean up first (in dependency order).
	ctx.Exec(nil, "DELETE FROM budget_links")
	ctx.Exec(nil, "DELETE FROM budget_expenses")
	ctx.Exec(nil, "DELETE FROM budget_subcategories")
	ctx.Exec(nil, "DELETE FROM budget_categories")
	ctx.Exec(nil, "DELETE FROM budget_collaborators")
	ctx.Exec(nil, "DELETE FROM budget_invites")
	ctx.Exec(nil, "DELETE FROM budgets")
	ctx.Exec(nil, "DELETE FROM user_sessions")
	ctx.Exec(nil, "DELETE FROM profiles")

	// Profiles.
	for _, u := range []struct {
		id    uuid.UUID
		email string
		name  string
	}{
		{ownerUserID, "owner@test.com", "Owner"},
		{collabUserID, "collab@test.com", "Collaborator"},
		{thirdUserID, "third@test.com", "Third"},
	} {
		_, err := ctx.Exec(nil,
			`INSERT INTO profiles (id, email, full_name, password_hash, auth_provider)
			 VALUES ($1, $2, $3, 'hash', 'email')`,
			u.id, u.email, u.name)
		if err != nil {
			t.Fatalf("seed profile %s: %v", u.email, err)
		}
	}

	// Budgets — all USD.
	for _, b := range []struct {
		id    uuid.UUID
		owner uuid.UUID
		name  string
	}{
		{budgetAID, ownerUserID, "Budget A"},
		{budgetBID, collabUserID, "Budget B"},
		{budgetCID, thirdUserID, "Budget C"},
	} {
		_, err := ctx.Exec(nil,
			`INSERT INTO budgets (id, user_id, name, monthly_income, currency, billing_period_months, billing_cutoff_day, mode)
			 VALUES ($1, $2, $3, 5000000, 'USD', 1, 1, 'manual')`,
			b.id, b.owner, b.name)
		if err != nil {
			t.Fatalf("seed budget %s: %v", b.name, err)
		}
	}

	// Sections.
	for _, s := range []struct {
		id       uuid.UUID
		budgetID uuid.UUID
		name     string
	}{
		{sectionA1ID, budgetAID, "Section A1"},
		{sectionB1ID, budgetBID, "Section B1"},
	} {
		_, err := ctx.Exec(nil,
			`INSERT INTO budget_categories (id, budget_id, name, allocation_value, icon, sort_order)
			 VALUES ($1, $2, $3, 50, 'home', 1)`,
			s.id, s.budgetID, s.name)
		if err != nil {
			t.Fatalf("seed section %s: %v", s.name, err)
		}
	}

	// Category in section A1.
	_, err := ctx.Exec(nil,
		`INSERT INTO budget_subcategories (id, category_id, name, allocation_value, icon, sort_order)
		 VALUES ($1, $2, 'Cat A1a', 100, 'tag', 1)`,
		categoryA1aID, sectionA1ID)
	if err != nil {
		t.Fatalf("seed category: %v", err)
	}
}

// =====================================================================
// Test 1: Cannot link a budget to itself
// =====================================================================

func TestCreateLink_CannotLinkToSelf(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	payload := `{
		"source_budget_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"source_section_id": "a1a1a1a1-a1a1-a1a1-a1a1-a1a1a1a1a1a1",
		"filter_mode": "all"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}

	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("self-link: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	var errResp models.ErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil {
		if !strings.Contains(errResp.Error, "itself") {
			t.Errorf("error message should mention 'itself', got: %q", errResp.Error)
		}
	}
}

// =====================================================================
// Test 2: Invalid filter_mode rejected
// =====================================================================

func TestCreateLink_InvalidFilterMode(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Give owner access to budget B (as collaborator).
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	payload := `{
		"source_budget_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"source_section_id": "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1",
		"filter_mode": "invalid"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("invalid filter_mode: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test 3: User must have access to BOTH source and target budgets
// =====================================================================

func TestCreateLink_SourceBudgetAccessRequired(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Owner does NOT have access to budget B (no collab record).
	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	payload := `{
		"source_budget_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"source_section_id": "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1",
		"filter_mode": "all"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("no source access: status = %d, want 404, body: %s", resp.StatusCode, string(body))
	}
}

func TestCreateLink_TargetBudgetAccessRequired(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// collabUser owns budget B but has no access to budget A.
	token := tokenForUser(collabUserID, "collab@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	payload := `{
		"source_budget_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"source_section_id": "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1",
		"filter_mode": "all"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("no target access: status = %d, want 404, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test 4: Cross-budget currency mismatch rejected
// =====================================================================

func TestCreateLink_CurrencyMismatchRejected(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Change budget B to a different currency.
	database.DB.Pool.Exec(nil,
		`UPDATE budgets SET currency = 'EUR' WHERE id = $1`, budgetBID)

	// Give owner access to budget B.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	payload := `{
		"source_budget_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"source_section_id": "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1",
		"filter_mode": "all"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("currency mismatch: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test 5: Successful link creation
// =====================================================================

func TestCreateLink_Success(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Give owner access to budget B.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	payload := `{
		"source_budget_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"source_section_id": "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1",
		"filter_mode": "all"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create link: status = %d, want 201, body: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	var link models.BudgetLink
	if err := json.Unmarshal(body, &link); err != nil {
		t.Fatalf("unmarshal link: %v", err)
	}

	if link.SourceBudgetID != budgetBID {
		t.Errorf("source_budget_id = %s, want %s", link.SourceBudgetID, budgetBID)
	}
	if link.TargetBudgetID != budgetAID {
		t.Errorf("target_budget_id = %s, want %s", link.TargetBudgetID, budgetAID)
	}
	if link.FilterMode != "all" {
		t.Errorf("filter_mode = %q, want 'all'", link.FilterMode)
	}
	if link.CreatedBy != ownerUserID {
		t.Errorf("created_by = %s, want %s", link.CreatedBy, ownerUserID)
	}
}

// =====================================================================
// Test 6: Per-budget link limit enforced
// =====================================================================

func TestCreateLink_MaxLinksPerBudgetEnforced(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Give owner access to budget B.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	// Insert 10 links (the max) directly in the DB.
	for i := 0; i < maxLinksPerBudget; i++ {
		sectionID := uuid.New()
		database.DB.Pool.Exec(nil,
			`INSERT INTO budget_categories (id, budget_id, name, allocation_value, icon, sort_order)
			 VALUES ($1, $2, $3, 10, 'tag', $4)`,
			sectionID, budgetBID, "DummySection"+string(rune('0'+i)), i+10)

		database.DB.Pool.Exec(nil,
			`INSERT INTO budget_links (source_budget_id, target_budget_id, source_section_id, filter_mode, created_by)
			 VALUES ($1, $2, $3, 'all', $4)`,
			budgetBID, budgetAID, sectionID, ownerUserID)
	}

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	payload := `{
		"source_budget_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"source_section_id": "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1",
		"filter_mode": "all"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("link limit: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test 7: Full-section and category-level links are mutually exclusive
// =====================================================================

func TestCreateLink_MutualExclusivity_FullThenCategory(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Give owner access to budget B.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	// Create a category in section B1 for the category-level link.
	catB1a := uuid.New()
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_subcategories (id, category_id, name, allocation_value, icon, sort_order)
		 VALUES ($1, $2, 'Cat B1a', 100, 'tag', 1)`,
		catB1a, sectionB1ID)

	// Insert a full-section link (source_category_id IS NULL).
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_links (source_budget_id, target_budget_id, source_section_id, filter_mode, created_by)
		 VALUES ($1, $2, $3, 'all', $4)`,
		budgetBID, budgetAID, sectionB1ID, ownerUserID)

	// Now try to create a category-level link for the same section — should fail.
	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	// We also need a target section in budget A for the category link.
	payload := `{
		"source_budget_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"source_section_id": "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1",
		"source_category_id": "` + catB1a.String() + `",
		"target_section_id": "a1a1a1a1-a1a1-a1a1-a1a1-a1a1a1a1a1a1",
		"filter_mode": "all"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("mutual exclusivity full→cat: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

func TestCreateLink_MutualExclusivity_CategoryThenFull(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Give owner access to budget B.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	// Create a category in section B1 for the category-level link.
	catB1a := uuid.New()
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_subcategories (id, category_id, name, allocation_value, icon, sort_order)
		 VALUES ($1, $2, 'Cat B1a', 100, 'tag', 1)`,
		catB1a, sectionB1ID)

	// Insert a category-level link first.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_links (source_budget_id, target_budget_id, source_section_id, source_category_id, target_section_id, filter_mode, created_by)
		 VALUES ($1, $2, $3, $4, $5, 'all', $6)`,
		budgetBID, budgetAID, sectionB1ID, catB1a, sectionA1ID, ownerUserID)

	// Now try to create a full-section link for the same section — should fail.
	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	payload := `{
		"source_budget_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"source_section_id": "b1b1b1b1-b1b1-b1b1-b1b1-b1b1b1b1b1b1",
		"filter_mode": "all"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("mutual exclusivity cat→full: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test 8: Unauthorized access to link endpoints
// =====================================================================

func TestListLinks_Unauthorized(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	app.Get("/api/budgets/:id/links", ListLinks) // No Protected middleware

	req := httptest.NewRequest(http.MethodGet, "/api/budgets/"+budgetAID.String()+"/links", nil)
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth: status = %d, want 401", resp.StatusCode)
	}
}

func TestCreateLink_Unauthorized(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)

	app.Post("/api/budgets/:id/links", CreateLink) // No Protected middleware

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth create: status = %d, want 401", resp.StatusCode)
	}
}

// =====================================================================
// Test 9: UpdateLink changes filter_mode
// =====================================================================

func TestUpdateLink_Success(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Give owner access to budget B and create a link.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	linkID := uuid.New()
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_links (id, source_budget_id, target_budget_id, source_section_id, filter_mode, created_by)
		 VALUES ($1, $2, $3, $4, 'all', $5)`,
		linkID, budgetBID, budgetAID, sectionB1ID, ownerUserID)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Patch("/api/budgets/:id/links/:linkId", middleware.Protected(), UpdateLink)

	payload := `{"filter_mode": "mine"}`
	req := httptest.NewRequest(http.MethodPatch,
		"/api/budgets/"+budgetAID.String()+"/links/"+linkID.String(),
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("update link: status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}

	body, _ := io.ReadAll(resp.Body)
	var link models.BudgetLink
	if err := json.Unmarshal(body, &link); err != nil {
		t.Fatalf("unmarshal link: %v", err)
	}
	if link.FilterMode != "mine" {
		t.Errorf("filter_mode = %q, want 'mine'", link.FilterMode)
	}
}

func TestUpdateLink_InvalidFilterMode(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	linkID := uuid.New()
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_links (id, source_budget_id, target_budget_id, source_section_id, filter_mode, created_by)
		 VALUES ($1, $2, $3, $4, 'all', $5)`,
		linkID, budgetBID, budgetAID, sectionB1ID, ownerUserID)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Patch("/api/budgets/:id/links/:linkId", middleware.Protected(), UpdateLink)

	payload := `{"filter_mode": "everyone"}`
	req := httptest.NewRequest(http.MethodPatch,
		"/api/budgets/"+budgetAID.String()+"/links/"+linkID.String(),
		strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("invalid filter_mode update: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test 10: DeleteLink removes the link
// =====================================================================

func TestDeleteLink_Success(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	linkID := uuid.New()
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_links (id, source_budget_id, target_budget_id, source_section_id, filter_mode, created_by)
		 VALUES ($1, $2, $3, $4, 'all', $5)`,
		linkID, budgetBID, budgetAID, sectionB1ID, ownerUserID)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Delete("/api/budgets/:id/links/:linkId", middleware.Protected(), DeleteLink)

	req := httptest.NewRequest(http.MethodDelete,
		"/api/budgets/"+budgetAID.String()+"/links/"+linkID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("delete link: status = %d, want 204, body: %s", resp.StatusCode, string(body))
	}

	// Verify it's actually gone.
	var count int
	database.DB.Pool.QueryRow(nil,
		"SELECT COUNT(*) FROM budget_links WHERE id = $1", linkID).Scan(&count)
	if count != 0 {
		t.Errorf("link still exists after delete, count = %d", count)
	}
}

func TestDeleteLink_NotFound(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Delete("/api/budgets/:id/links/:linkId", middleware.Protected(), DeleteLink)

	fakeLink := uuid.New()
	req := httptest.NewRequest(http.MethodDelete,
		"/api/budgets/"+budgetAID.String()+"/links/"+fakeLink.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("delete nonexistent link: status = %d, want 404, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test 11: Source section must belong to source budget
// =====================================================================

func TestCreateLink_SectionMustBelongToSourceBudget(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Give owner access to budget B.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role) VALUES ($1, $2, 'collaborator')
		 ON CONFLICT DO NOTHING`,
		budgetBID, ownerUserID)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	// Try to use sectionA1 (belongs to budget A) as source for budget B.
	payload := `{
		"source_budget_id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		"source_section_id": "a1a1a1a1-a1a1-a1a1-a1a1-a1a1a1a1a1a1",
		"filter_mode": "all"
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/links", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("wrong section for source: status = %d, want 404, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test 12: Invalid budget ID parameter rejected
// =====================================================================

func TestCreateLink_InvalidBudgetIDParam(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)

	token := tokenForUser(ownerUserID, "owner@test.com")
	app.Post("/api/budgets/:id/links", middleware.Protected(), CreateLink)

	req := httptest.NewRequest(http.MethodPost, "/api/budgets/not-a-uuid/links", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid budget id: status = %d, want 400", resp.StatusCode)
	}
}

// =====================================================================
// Test 13: DB constraint — chk_different_budgets
// =====================================================================

func TestDBConstraint_DifferentBudgets(t *testing.T) {
	_, _ = setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Attempt to insert a link where source == target directly via SQL.
	_, err := database.DB.Pool.Exec(nil,
		`INSERT INTO budget_links (source_budget_id, target_budget_id, source_section_id, filter_mode, created_by)
		 VALUES ($1, $1, $2, 'all', $3)`,
		budgetAID, sectionA1ID, ownerUserID)

	if err == nil {
		t.Error("expected DB constraint violation for same-budget link, got nil")
	}
}

// =====================================================================
// Test 14: DB constraint — chk_filter_mode
// =====================================================================

func TestDBConstraint_FilterMode(t *testing.T) {
	_, _ = setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	_, err := database.DB.Pool.Exec(nil,
		`INSERT INTO budget_links (source_budget_id, target_budget_id, source_section_id, filter_mode, created_by)
		 VALUES ($1, $2, $3, 'invalid', $4)`,
		budgetBID, budgetAID, sectionB1ID, ownerUserID)

	if err == nil {
		t.Error("expected DB constraint violation for invalid filter_mode, got nil")
	}
}

// =====================================================================
// Test 15: Unique constraint — cannot duplicate a link
// =====================================================================

func TestDBConstraint_UniqueLink(t *testing.T) {
	_, _ = setupLinkSecurityEnv(t)
	seedLinkTestData(t)

	// Insert first link.
	_, err := database.DB.Pool.Exec(nil,
		`INSERT INTO budget_links (source_budget_id, target_budget_id, source_section_id, filter_mode, created_by)
		 VALUES ($1, $2, $3, 'all', $4)`,
		budgetBID, budgetAID, sectionB1ID, ownerUserID)
	if err != nil {
		t.Fatalf("first link insert: %v", err)
	}

	// Duplicate should fail.
	_, err = database.DB.Pool.Exec(nil,
		`INSERT INTO budget_links (source_budget_id, target_budget_id, source_section_id, filter_mode, created_by)
		 VALUES ($1, $2, $3, 'all', $4)`,
		budgetBID, budgetAID, sectionB1ID, ownerUserID)
	if err == nil {
		t.Error("expected uniqueness violation for duplicate link, got nil")
	}
}
