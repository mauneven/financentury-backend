package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// Package-level frontend URL for invite links.
var frontendURL string

// InitInvites configures the invites handler with the frontend URL.
func InitInvites(url string) {
	frontendURL = url
}

// CreateInvite generates a new invite link for a budget (owner only).
func CreateInvite(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}

	// Verify the user is the budget owner (budgets.user_id = authenticated user).
	if err := verifyBudgetOwnership(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Generate a unique invite token: 32 bytes -> hex encode.
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to generate invite token"})
	}
	inviteToken := hex.EncodeToString(tokenBytes)

	now := time.Now().UTC()
	expiresAt := now.Add(7 * 24 * time.Hour)
	inviteID := uuid.New()

	payload := map[string]interface{}{
		"id":           inviteID.String(),
		"budget_id":    budgetID.String(),
		"invite_token": inviteToken,
		"created_by":   userID.String(),
		"expires_at":   expiresAt.Format(time.RFC3339Nano),
		"created_at":   now.Format(time.RFC3339Nano),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to serialize request"})
	}

	_, statusCode, err := database.DB.Post("budget_invites", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to create invite"})
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"invite_token": inviteToken,
		"invite_url":   frontendURL + "/invite/" + inviteToken,
		"expires_at":   expiresAt.Format(time.RFC3339Nano),
	})
}

// GetInviteInfo returns public invite preview info (no auth required).
func GetInviteInfo(c *fiber.Ctx) error {
	token := c.Params("token")
	if token == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invite token is required"})
	}
	if len(token) > 128 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid invite token"})
	}

	// Fetch the invite by token.
	query := database.NewFilter().
		Select("*").
		Eq("invite_token", token).
		Build()

	body, statusCode, err := database.DB.Get("budget_invites", query)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch invite"})
	}

	var invites []models.Invite
	if err := json.Unmarshal(body, &invites); err != nil || len(invites) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "invite not found"})
	}

	invite := invites[0]

	// Fetch budget name.
	budgetQuery := database.NewFilter().
		Select("name").
		Eq("id", invite.BudgetID.String()).
		Build()

	budgetBody, statusCode, err := database.DB.Get("budgets", budgetQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch budget"})
	}

	var budgets []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(budgetBody, &budgets); err != nil || len(budgets) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Fetch inviter name.
	profileQuery := database.NewFilter().
		Select("full_name").
		Eq("id", invite.CreatedBy.String()).
		Build()

	profileBody, statusCode, err := database.DB.Get("profiles", profileQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch inviter profile"})
	}

	var profiles []struct {
		FullName string `json:"full_name"`
	}
	inviterName := "Unknown"
	if err := json.Unmarshal(profileBody, &profiles); err == nil && len(profiles) > 0 {
		inviterName = profiles[0].FullName
	}

	// Determine if expired or used.
	isExpired := false
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, invite.ExpiresAt)
	if parseErr == nil && time.Now().UTC().After(expiresAt) {
		isExpired = true
	}
	// Also try parsing without nano precision.
	if parseErr != nil {
		expiresAt, parseErr = time.Parse(time.RFC3339, invite.ExpiresAt)
		if parseErr == nil && time.Now().UTC().After(expiresAt) {
			isExpired = true
		}
	}

	isUsed := invite.UsedBy != nil

	return c.JSON(models.InviteInfo{
		BudgetName:  budgets[0].Name,
		InviterName: inviterName,
		ExpiresAt:   invite.ExpiresAt,
		IsExpired:   isExpired,
		IsUsed:      isUsed,
	})
}

// AcceptInvite accepts an invite and adds the user as a collaborator.
func AcceptInvite(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}

	token := c.Params("token")
	if token == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invite token is required"})
	}
	if len(token) > 128 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid invite token"})
	}

	// Fetch the invite by token.
	query := database.NewFilter().
		Select("*").
		Eq("invite_token", token).
		Build()

	body, statusCode, err := database.DB.Get("budget_invites", query)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch invite"})
	}

	var invites []models.Invite
	if err := json.Unmarshal(body, &invites); err != nil || len(invites) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "invite not found"})
	}

	invite := invites[0]

	// Check if already used.
	if invite.UsedBy != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invite has already been used"})
	}

	// Check if expired.
	expiresAt, parseErr := time.Parse(time.RFC3339Nano, invite.ExpiresAt)
	if parseErr != nil {
		expiresAt, parseErr = time.Parse(time.RFC3339, invite.ExpiresAt)
	}
	if parseErr == nil && time.Now().UTC().After(expiresAt) {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invite has expired"})
	}

	// Check if the user is the budget owner (can't join own budget).
	ownerQuery := database.NewFilter().
		Select("id").
		Eq("id", invite.BudgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	ownerBody, statusCode, err := database.DB.Get("budgets", ownerQuery)
	if err == nil && statusCode == http.StatusOK {
		var ownerFound []struct{ ID string `json:"id"` }
		if err := json.Unmarshal(ownerBody, &ownerFound); err == nil && len(ownerFound) > 0 {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "you are already the owner of this budget"})
		}
	}

	// Check if user is already a collaborator.
	collabCheckQuery := database.NewFilter().
		Select("id").
		Eq("budget_id", invite.BudgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	collabBody, statusCode, err := database.DB.Get("budget_collaborators", collabCheckQuery)
	if err == nil && statusCode == http.StatusOK {
		var collabFound []struct{ ID string `json:"id"` }
		if err := json.Unmarshal(collabBody, &collabFound); err == nil && len(collabFound) > 0 {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "you are already a collaborator on this budget"})
		}
	}

	now := time.Now().UTC()

	// Mark invite as used.
	usedPayload := map[string]interface{}{
		"used_by": userID.String(),
		"used_at": now.Format(time.RFC3339Nano),
	}
	usedBytes, err := json.Marshal(usedPayload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to serialize request"})
	}

	patchQuery := database.NewFilter().
		Eq("id", invite.ID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budget_invites", patchQuery, usedBytes)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to update invite"})
	}

	// Add user to budget_collaborators.
	collabID := uuid.New()
	collabPayload := map[string]interface{}{
		"id":        collabID.String(),
		"budget_id": invite.BudgetID.String(),
		"user_id":   userID.String(),
		"role":      "collaborator",
		"added_at":  now.Format(time.RFC3339Nano),
	}
	collabBytes, err := json.Marshal(collabPayload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to serialize request"})
	}

	_, statusCode, err = database.DB.Post("budget_collaborators", collabBytes)
	if err != nil || statusCode != http.StatusCreated {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to add collaborator"})
	}

	// Return the budget data.
	budgetQuery := database.NewFilter().
		Select("*").
		Eq("id", invite.BudgetID.String()).
		Build()

	budgetBody, statusCode, err := database.DB.Get("budgets", budgetQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch budget"})
	}

	var budgets []models.Budget
	if err := json.Unmarshal(budgetBody, &budgets); err != nil || len(budgets) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	return c.JSON(budgets[0])
}
