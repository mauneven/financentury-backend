package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// Package-level frontend URL for invite links.
var frontendURL string

// maxInviteTokenLength is the upper bound for invite token query parameters.
const maxInviteTokenLength = 128

// inviteTokenBytes is the number of random bytes used for invite tokens.
const inviteTokenBytes = 32

// inviteExpiry is how long an invite link remains valid.
const inviteExpiry = 7 * 24 * time.Hour

// maxCollaboratorsPerBudget is the cap on collaborators (excluding the owner).
const maxCollaboratorsPerBudget = 5

// countBudgetCollaborators returns the current number of collaborators for a budget
// using a COUNT(*) query instead of fetching all rows.
func countBudgetCollaborators(budgetID uuid.UUID) (int, error) {
	var count int
	err := database.DB.Pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM budget_collaborators WHERE budget_id = $1`, budgetID).Scan(&count)
	return count, err
}

// InitInvites configures the invites handler with the frontend URL used to
// construct invite links.
func InitInvites(url string) {
	frontendURL = url
}

// ListInvites returns all invites for a budget (owner only).
func ListInvites(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	if err := verifyBudgetOwnership(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	query := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("created_at", "desc").
		Build()

	body, statusCode, err := database.DB.Get("budget_invites", query)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch invites")
	}

	var invites []models.Invite
	if err := json.Unmarshal(body, &invites); err != nil {
		return errInternal(c, "failed to parse invites")
	}

	if invites == nil {
		invites = make([]models.Invite, 0)
	}

	return c.JSON(invites)
}

// CreateInvite generates a new invite link for a budget (owner only).
func CreateInvite(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	if err := verifyBudgetOwnership(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Enforce max collaborators per budget.
	count, err := countBudgetCollaborators(budgetID)
	if err != nil {
		return errInternal(c, "failed to check collaborator count")
	}
	if count >= maxCollaboratorsPerBudget {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "collaborator limit reached (max 5)",
		})
	}

	// Generate a unique invite token.
	tokenBytes := make([]byte, inviteTokenBytes)
	if _, err := rand.Read(tokenBytes); err != nil {
		return errInternal(c, "failed to generate invite token")
	}
	inviteToken := hex.EncodeToString(tokenBytes)

	now := time.Now().UTC()
	expiresAt := now.Add(inviteExpiry)
	inviteID := uuid.New()

	payload := map[string]interface{}{
		"id":           inviteID.String(),
		"budget_id":    budgetID.String(),
		"invite_token": inviteToken,
		"created_by":   userID.String(),
		"expires_at":   expiresAt.Format(time.RFC3339Nano),
		"created_at":   now.Format(time.RFC3339Nano),
	}
	payloadBytes, err := marshalJSON(payload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	_, statusCode, err := database.DB.Post("budget_invites", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		return errInternal(c, "failed to create invite")
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"invite_token": inviteToken,
		"invite_url":   frontendURL + "/invite/" + inviteToken,
		"expires_at":   expiresAt.Format(time.RFC3339Nano),
	})
}

// GetInviteInfo returns public invite preview info (no auth required).
func GetInviteInfo(c *fiber.Ctx) error {
	token := strings.TrimSpace(c.Params("token"))
	if token == "" {
		return errBadRequest(c, "invite token is required")
	}
	if len(token) > maxInviteTokenLength {
		return errBadRequest(c, "invalid invite token")
	}

	// Fetch the invite by token.
	query := database.NewFilter().
		Select("*").
		Eq("invite_token", token).
		Build()

	body, statusCode, err := database.DB.Get("budget_invites", query)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch invite")
	}

	var invites []models.Invite
	if err := json.Unmarshal(body, &invites); err != nil || len(invites) == 0 {
		return errNotFound(c, "invite not found")
	}

	invite := invites[0]

	// Fetch budget name.
	budgetQuery := database.NewFilter().
		Select("name").
		Eq("id", invite.BudgetID.String()).
		Build()

	budgetBody, budgetStatus, budgetErr := database.DB.Get("budgets", budgetQuery)
	if budgetErr != nil || budgetStatus != http.StatusOK {
		return errInternal(c, "failed to fetch budget")
	}

	var budgets []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(budgetBody, &budgets); err != nil || len(budgets) == 0 {
		return errNotFound(c, "budget not found")
	}

	// Fetch inviter name.
	profileQuery := database.NewFilter().
		Select("full_name").
		Eq("id", invite.CreatedBy.String()).
		Build()

	profileBody, profileStatus, profileErr := database.DB.Get("profiles", profileQuery)
	inviterName := "Unknown"
	if profileErr == nil && profileStatus == http.StatusOK {
		var profiles []struct {
			FullName string `json:"full_name"`
		}
		if err := json.Unmarshal(profileBody, &profiles); err == nil && len(profiles) > 0 {
			inviterName = profiles[0].FullName
		}
	}

	// Determine expiry.
	isExpired := false
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, invite.ExpiresAt)
	if parseErr != nil {
		expiresAt, parseErr = time.Parse(time.RFC3339, invite.ExpiresAt)
	}
	if parseErr == nil && time.Now().UTC().After(expiresAt) {
		isExpired = true
	}

	return c.JSON(models.InviteInfo{
		BudgetName:  budgets[0].Name,
		InviterName: inviterName,
		ExpiresAt:   invite.ExpiresAt,
		IsExpired:   isExpired,
		IsUsed:      invite.UsedBy != nil,
	})
}

// AcceptInvite accepts an invite and adds the user as a collaborator.
// On success it broadcasts a collaborator_added event via WebSocket.
func AcceptInvite(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	token := strings.TrimSpace(c.Params("token"))
	if token == "" {
		return errBadRequest(c, "invite token is required")
	}
	if len(token) > maxInviteTokenLength {
		return errBadRequest(c, "invalid invite token")
	}

	// Fetch the invite by token.
	query := database.NewFilter().
		Select("*").
		Eq("invite_token", token).
		Build()

	body, statusCode, err := database.DB.Get("budget_invites", query)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch invite")
	}

	var invites []models.Invite
	if err := json.Unmarshal(body, &invites); err != nil || len(invites) == 0 {
		return errNotFound(c, "invite not found")
	}

	invite := invites[0]

	// Check expiry.
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, invite.ExpiresAt)
	if parseErr != nil {
		expiresAt, parseErr = time.Parse(time.RFC3339, invite.ExpiresAt)
	}
	if parseErr == nil && time.Now().UTC().After(expiresAt) {
		return errBadRequest(c, "invite has expired")
	}

	// Prevent owner from joining their own budget.
	ownerQuery := database.NewFilter().
		Select("id").
		Eq("id", invite.BudgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	ownerBody, ownerStatus, ownerErr := database.DB.Get("budgets", ownerQuery)
	if ownerErr == nil && ownerStatus == http.StatusOK {
		var ownerFound []struct{ ID string `json:"id"` }
		if err := json.Unmarshal(ownerBody, &ownerFound); err == nil && len(ownerFound) > 0 {
			return errBadRequest(c, "you are already the owner of this budget")
		}
	}

	// Enforce per-user budget limit before adding a new collaboration.
	if err := enforceUserBudgetLimit(userID); err != nil {
		if err.Error() == "limit" {
			return errBadRequest(c, "budget limit reached (max 7)")
		}
		return errInternal(c, "failed to check budget count")
	}

	// Prevent duplicate collaborator.
	collabCheckQuery := database.NewFilter().
		Select("id").
		Eq("budget_id", invite.BudgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	collabBody, collabStatus, collabErr := database.DB.Get("budget_collaborators", collabCheckQuery)
	if collabErr == nil && collabStatus == http.StatusOK {
		var collabFound []struct{ ID string `json:"id"` }
		if err := json.Unmarshal(collabBody, &collabFound); err == nil && len(collabFound) > 0 {
			return errBadRequest(c, "you are already a collaborator on this budget")
		}
	}

	// Mark invite as used AND insert the collaborator in the SAME transaction
	// so that if the collaborator insert fails (e.g., limit reached), the
	// invite is NOT consumed. Without this, a failed insert would leave the
	// invite permanently marked as used even though the user was never added.
	tx, err := database.DB.Pool.Begin(context.Background())
	if err != nil {
		return errInternal(c, "failed to start transaction")
	}
	defer tx.Rollback(context.Background())

	// Atomically mark invite as used — prevents race condition where two
	// concurrent requests both pass the used_by check.
	var inviteID string
	err = tx.QueryRow(context.Background(),
		`UPDATE budget_invites SET used_by = $1, used_at = NOW()
		 WHERE id = $2 AND used_by IS NULL
		 RETURNING id`,
		userID, invite.ID).Scan(&inviteID)
	if err != nil {
		return errBadRequest(c, "invite has already been used")
	}

	// Atomically add collaborator only if under the limit (prevents race condition).
	collabID := uuid.New()
	tag, insertErr := tx.Exec(context.Background(),
		`INSERT INTO budget_collaborators (id, budget_id, user_id, role)
		 SELECT $1, $2, $3, 'collaborator'
		 WHERE (SELECT COUNT(*) FROM budget_collaborators WHERE budget_id = $2) < $4`,
		collabID, invite.BudgetID, userID, maxCollaboratorsPerBudget)
	if insertErr != nil {
		return errInternal(c, "failed to add collaborator")
	}
	if tag.RowsAffected() == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "collaborator limit reached (max 5)",
		})
	}

	if err := tx.Commit(context.Background()); err != nil {
		return errInternal(c, "failed to commit invite acceptance")
	}

	// Return the budget data.
	budgetQuery := database.NewFilter().
		Select("*").
		Eq("id", invite.BudgetID.String()).
		Build()

	budgetBody, budgetStatus, budgetErr := database.DB.Get("budgets", budgetQuery)
	if budgetErr != nil || budgetStatus != http.StatusOK {
		return errInternal(c, "failed to fetch budget")
	}

	var budgets []models.Budget
	if err := json.Unmarshal(budgetBody, &budgets); err != nil || len(budgets) == 0 {
		return errNotFound(c, "budget not found")
	}

	broadcast(invite.BudgetID.String(), ws.MessageTypeCollabAdded, map[string]string{
		"user_id": userID.String(),
	})

	return c.JSON(budgets[0])
}
