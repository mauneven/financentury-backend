package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
)

// seedCollabTestData sets up the data needed for collaboration security tests.
// Creates three users, two budgets, and collaborator relationships.
func seedCollabTestData(t *testing.T) {
	t.Helper()
	pool := database.DB.Pool

	// Clean up (dependency order).
	pool.Exec(nil, "DELETE FROM budget_links")
	pool.Exec(nil, "DELETE FROM budget_expenses")
	pool.Exec(nil, "DELETE FROM budget_subcategories")
	pool.Exec(nil, "DELETE FROM budget_categories")
	pool.Exec(nil, "DELETE FROM budget_collaborators")
	pool.Exec(nil, "DELETE FROM budget_invites")
	pool.Exec(nil, "DELETE FROM budgets")
	pool.Exec(nil, "DELETE FROM user_sessions")
	pool.Exec(nil, "DELETE FROM profiles")

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
		_, err := pool.Exec(nil,
			`INSERT INTO profiles (id, email, full_name, password_hash, auth_provider)
			 VALUES ($1, $2, $3, 'hash', 'email')`,
			u.id, u.email, u.name)
		if err != nil {
			t.Fatalf("seed profile %s: %v", u.email, err)
		}
	}

	// Budget A owned by owner.
	_, err := pool.Exec(nil,
		`INSERT INTO budgets (id, user_id, name, monthly_income, currency, billing_period_months, billing_cutoff_day, mode)
		 VALUES ($1, $2, 'Budget A', 5000000, 'USD', 1, 1, 'manual')`,
		budgetAID, ownerUserID)
	if err != nil {
		t.Fatalf("seed budget A: %v", err)
	}

	// Budget B owned by collab user.
	_, err = pool.Exec(nil,
		`INSERT INTO budgets (id, user_id, name, monthly_income, currency, billing_period_months, billing_cutoff_day, mode)
		 VALUES ($1, $2, 'Budget B', 3000000, 'USD', 1, 1, 'manual')`,
		budgetBID, collabUserID)
	if err != nil {
		t.Fatalf("seed budget B: %v", err)
	}
}

// =====================================================================
// Test: Only budget owner can remove collaborators
// =====================================================================

func TestRemoveCollaborator_OnlyOwnerCanRemove(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	// Add collabUser as collaborator on budget A.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role)
		 VALUES ($1, $2, 'collaborator')`,
		budgetAID, collabUserID)

	app.Delete("/api/budgets/:id/collaborators/:userId", middleware.Protected(), RemoveCollaborator)

	// Collaborator tries to remove themselves — should fail (not owner).
	token := tokenForUser(collabUserID, "collab@test.com")
	req := httptest.NewRequest(http.MethodDelete,
		"/api/budgets/"+budgetAID.String()+"/collaborators/"+collabUserID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("non-owner remove: status = %d, want 403, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Owner cannot remove themselves
// =====================================================================

func TestRemoveCollaborator_OwnerCannotRemoveSelf(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	app.Delete("/api/budgets/:id/collaborators/:userId", middleware.Protected(), RemoveCollaborator)

	token := tokenForUser(ownerUserID, "owner@test.com")
	req := httptest.NewRequest(http.MethodDelete,
		"/api/budgets/"+budgetAID.String()+"/collaborators/"+ownerUserID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("owner self-remove: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Removing collaborator succeeds for owner
// =====================================================================

func TestRemoveCollaborator_OwnerSuccess(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role)
		 VALUES ($1, $2, 'collaborator')`,
		budgetAID, collabUserID)

	app.Delete("/api/budgets/:id/collaborators/:userId", middleware.Protected(), RemoveCollaborator)

	token := tokenForUser(ownerUserID, "owner@test.com")
	req := httptest.NewRequest(http.MethodDelete,
		"/api/budgets/"+budgetAID.String()+"/collaborators/"+collabUserID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("owner remove collab: status = %d, want 204, body: %s", resp.StatusCode, string(body))
	}

	// Verify the collaborator is actually removed.
	var count int
	database.DB.Pool.QueryRow(nil,
		"SELECT COUNT(*) FROM budget_collaborators WHERE budget_id = $1 AND user_id = $2",
		budgetAID, collabUserID).Scan(&count)
	if count != 0 {
		t.Errorf("collaborator still exists after removal, count = %d", count)
	}
}

// =====================================================================
// Test: Removing collaborator preserves their expense data
// =====================================================================

func TestRemoveCollaborator_PreservesExpenseData(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	pool := database.DB.Pool

	// Add collab as collaborator.
	pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role)
		 VALUES ($1, $2, 'collaborator')`,
		budgetAID, collabUserID)

	// Create a section and category in budget A.
	sectionID := uuid.New()
	categoryID := uuid.New()
	pool.Exec(nil,
		`INSERT INTO budget_categories (id, budget_id, name, allocation_value, icon, sort_order)
		 VALUES ($1, $2, 'Test Section', 100, 'home', 1)`,
		sectionID, budgetAID)
	pool.Exec(nil,
		`INSERT INTO budget_subcategories (id, category_id, name, allocation_value, icon, sort_order)
		 VALUES ($1, $2, 'Test Cat', 100, 'tag', 1)`,
		categoryID, sectionID)

	// Collaborator creates an expense.
	expenseID := uuid.New()
	pool.Exec(nil,
		`INSERT INTO budget_expenses (id, budget_id, subcategory_id, amount, description, expense_date, created_by)
		 VALUES ($1, $2, $3, 150.00, 'Collab expense', '2026-04-10', $4)`,
		expenseID, budgetAID, categoryID, collabUserID)

	// Remove the collaborator.
	app.Delete("/api/budgets/:id/collaborators/:userId", middleware.Protected(), RemoveCollaborator)

	token := tokenForUser(ownerUserID, "owner@test.com")
	req := httptest.NewRequest(http.MethodDelete,
		"/api/budgets/"+budgetAID.String()+"/collaborators/"+collabUserID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("remove collab for preserve test: status = %d, body: %s", resp.StatusCode, string(body))
	}

	// Verify the expense is still there.
	var count int
	pool.QueryRow(nil,
		"SELECT COUNT(*) FROM budget_expenses WHERE id = $1", expenseID).Scan(&count)
	if count != 1 {
		t.Errorf("expense should be preserved after collaborator removal, got count = %d", count)
	}
}

// =====================================================================
// Test: Collaborator cannot delete owner's sections
// =====================================================================

func TestCollaborator_CannotDeleteOwnerSection(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	pool := database.DB.Pool

	// Add collab as collaborator on budget A.
	pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role)
		 VALUES ($1, $2, 'collaborator')`,
		budgetAID, collabUserID)

	// Owner creates a section.
	sectionID := uuid.New()
	pool.Exec(nil,
		`INSERT INTO budget_categories (id, budget_id, name, allocation_value, icon, sort_order)
		 VALUES ($1, $2, 'Owner Section', 100, 'home', 1)`,
		sectionID, budgetAID)

	// DeleteSection requires ownership, not just access.
	app.Delete("/api/budgets/:id/sections/:sectionId", middleware.Protected(), DeleteSection)

	// Collaborator tries to delete the section.
	token := tokenForUser(collabUserID, "collab@test.com")
	req := httptest.NewRequest(http.MethodDelete,
		"/api/budgets/"+budgetAID.String()+"/sections/"+sectionID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	// DeleteSection uses verifyBudgetOwnership — collab should get 404 (not owner).
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("collab delete owner section: status = %d, want 404, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Invite limits — max 5 collaborators
// =====================================================================

func TestAcceptInvite_CollaboratorLimitReached(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	pool := database.DB.Pool

	// Add 5 collaborators to budget A.
	for i := 0; i < maxCollaboratorsPerBudget; i++ {
		uid := uuid.New()
		pool.Exec(nil,
			`INSERT INTO profiles (id, email, full_name, password_hash, auth_provider)
			 VALUES ($1, $2, $3, 'hash', 'email')`,
			uid, fmt.Sprintf("user%d@test.com", i), fmt.Sprintf("User %d", i))
		pool.Exec(nil,
			`INSERT INTO budget_collaborators (budget_id, user_id, role)
			 VALUES ($1, $2, 'collaborator')`,
			budgetAID, uid)
	}

	// Create an invite for budget A.
	inviteToken := "test-invite-limit-token"
	inviteID := uuid.New()
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	pool.Exec(nil,
		`INSERT INTO budget_invites (id, budget_id, invite_token, created_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		inviteID, budgetAID, inviteToken, ownerUserID, expiresAt)

	app.Post("/api/invites/:token/accept", middleware.Protected(), AcceptInvite)

	// thirdUser tries to accept — should be rejected.
	token := tokenForUser(thirdUserID, "third@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/invites/"+inviteToken+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("collab limit: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Invite expiration
// =====================================================================

func TestAcceptInvite_Expired(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	pool := database.DB.Pool

	// Create an expired invite.
	inviteToken := "test-expired-invite-token"
	inviteID := uuid.New()
	expiredAt := time.Now().UTC().Add(-24 * time.Hour) // expired yesterday
	pool.Exec(nil,
		`INSERT INTO budget_invites (id, budget_id, invite_token, created_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		inviteID, budgetAID, inviteToken, ownerUserID, expiredAt)

	app.Post("/api/invites/:token/accept", middleware.Protected(), AcceptInvite)

	token := tokenForUser(thirdUserID, "third@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/invites/"+inviteToken+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expired invite: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}

	var errResp struct{ Error string `json:"error"` }
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &errResp); err == nil {
		if !strings.Contains(errResp.Error, "expired") {
			t.Errorf("error should mention 'expired', got: %q", errResp.Error)
		}
	}
}

// =====================================================================
// Test: Single-use invites — used invites cannot be reused
// =====================================================================

func TestAcceptInvite_AlreadyUsed(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	pool := database.DB.Pool

	// Create an invite that's already been used.
	inviteToken := "test-used-invite-token"
	inviteID := uuid.New()
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	usedAt := time.Now().UTC()
	pool.Exec(nil,
		`INSERT INTO budget_invites (id, budget_id, invite_token, created_by, used_by, used_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		inviteID, budgetAID, inviteToken, ownerUserID, collabUserID, usedAt, expiresAt)

	app.Post("/api/invites/:token/accept", middleware.Protected(), AcceptInvite)

	token := tokenForUser(thirdUserID, "third@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/invites/"+inviteToken+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("used invite: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Owner cannot accept their own invite
// =====================================================================

func TestAcceptInvite_OwnerCannotAccept(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	pool := database.DB.Pool

	inviteToken := "test-owner-accept-token"
	inviteID := uuid.New()
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	pool.Exec(nil,
		`INSERT INTO budget_invites (id, budget_id, invite_token, created_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		inviteID, budgetAID, inviteToken, ownerUserID, expiresAt)

	app.Post("/api/invites/:token/accept", middleware.Protected(), AcceptInvite)

	// Owner tries to accept their own invite.
	token := tokenForUser(ownerUserID, "owner@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/invites/"+inviteToken+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("owner accept own invite: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Duplicate collaborator prevented
// =====================================================================

func TestAcceptInvite_DuplicateCollaborator(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	pool := database.DB.Pool

	// Add thirdUser as collaborator already.
	pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role)
		 VALUES ($1, $2, 'collaborator')`,
		budgetAID, thirdUserID)

	inviteToken := "test-dupe-collab-token"
	inviteID := uuid.New()
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	pool.Exec(nil,
		`INSERT INTO budget_invites (id, budget_id, invite_token, created_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		inviteID, budgetAID, inviteToken, ownerUserID, expiresAt)

	app.Post("/api/invites/:token/accept", middleware.Protected(), AcceptInvite)

	token := tokenForUser(thirdUserID, "third@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/invites/"+inviteToken+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("duplicate collab: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Invite token length limit
// =====================================================================

func TestAcceptInvite_TokenTooLong(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)

	app.Post("/api/invites/:token/accept", middleware.Protected(), AcceptInvite)

	longToken := strings.Repeat("a", maxInviteTokenLength+1)
	token := tokenForUser(thirdUserID, "third@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/invites/"+longToken+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("token too long: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Invite not found (invalid token)
// =====================================================================

func TestAcceptInvite_NotFound(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	app.Post("/api/invites/:token/accept", middleware.Protected(), AcceptInvite)

	token := tokenForUser(thirdUserID, "third@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/invites/nonexistent-token/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("invite not found: status = %d, want 404, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Successful invite acceptance
// =====================================================================

func TestAcceptInvite_Success(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	pool := database.DB.Pool

	inviteToken := "test-success-accept-token"
	inviteID := uuid.New()
	expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
	pool.Exec(nil,
		`INSERT INTO budget_invites (id, budget_id, invite_token, created_by, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		inviteID, budgetAID, inviteToken, ownerUserID, expiresAt)

	app.Post("/api/invites/:token/accept", middleware.Protected(), AcceptInvite)

	token := tokenForUser(thirdUserID, "third@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/invites/"+inviteToken+"/accept", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("accept invite: status = %d, want 200, body: %s", resp.StatusCode, string(body))
	}

	// Verify collaborator was added.
	var count int
	pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM budget_collaborators WHERE budget_id = $1 AND user_id = $2",
		budgetAID, thirdUserID).Scan(&count)
	if count != 1 {
		t.Errorf("collaborator should exist after acceptance, count = %d", count)
	}

	// Verify invite was marked as used.
	var usedBy *uuid.UUID
	pool.QueryRow(context.Background(),
		"SELECT used_by FROM budget_invites WHERE id = $1",
		inviteID).Scan(&usedBy)
	if usedBy == nil || *usedBy != thirdUserID {
		t.Errorf("invite used_by = %v, want %s", usedBy, thirdUserID)
	}
}

// =====================================================================
// Test: Create invite — only owner can create
// =====================================================================

func TestCreateInvite_OnlyOwner(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	// Add collab as collaborator on budget A.
	database.DB.Pool.Exec(nil,
		`INSERT INTO budget_collaborators (budget_id, user_id, role)
		 VALUES ($1, $2, 'collaborator')`,
		budgetAID, collabUserID)

	app.Post("/api/budgets/:id/invites", middleware.Protected(), CreateInvite)

	// Collaborator tries to create invite — should fail.
	token := tokenForUser(collabUserID, "collab@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/invites", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	// CreateInvite uses verifyBudgetOwnership, so non-owner gets 404.
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("non-owner create invite: status = %d, want 404, body: %s", resp.StatusCode, string(body))
	}
}

// =====================================================================
// Test: Create invite — limit enforcement at invite creation time
// =====================================================================

func TestCreateInvite_CollaboratorLimitAtCreation(t *testing.T) {
	app, _ := setupLinkSecurityEnv(t)
	seedCollabTestData(t)

	pool := database.DB.Pool

	// Add 5 collaborators (the max).
	for i := 0; i < maxCollaboratorsPerBudget; i++ {
		uid := uuid.New()
		pool.Exec(nil,
			`INSERT INTO profiles (id, email, full_name, password_hash, auth_provider)
			 VALUES ($1, $2, $3, 'hash', 'email')`,
			uid, fmt.Sprintf("fill%d@test.com", i), fmt.Sprintf("Fill %d", i))
		pool.Exec(nil,
			`INSERT INTO budget_collaborators (budget_id, user_id, role)
			 VALUES ($1, $2, 'collaborator')`,
			budgetAID, uid)
	}

	app.Post("/api/budgets/:id/invites", middleware.Protected(), CreateInvite)

	token := tokenForUser(ownerUserID, "owner@test.com")
	req := httptest.NewRequest(http.MethodPost, "/api/budgets/"+budgetAID.String()+"/invites", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, _ := app.Test(req)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("invite at limit: status = %d, want 400, body: %s", resp.StatusCode, string(body))
	}
}
