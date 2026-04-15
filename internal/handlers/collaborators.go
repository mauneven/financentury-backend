package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// ListCollaborators returns all collaborators for a budget (owner or
// collaborator). Each collaborator record is enriched with profile info.
func ListCollaborators(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Fetch the budget to get the owner user_id.
	budgetQuery := database.NewFilter().
		Select("user_id").
		Eq("id", budgetID.String()).
		Build()

	budgetBody, budgetStatus, budgetErr := database.DB.Get("budgets", budgetQuery)
	if budgetErr != nil || budgetStatus != http.StatusOK {
		return errInternal(c, "failed to fetch budget")
	}

	var budgetRows []struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(budgetBody, &budgetRows); err != nil || len(budgetRows) == 0 {
		return errNotFound(c, "budget not found")
	}

	ownerID := budgetRows[0].UserID

	query := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("added_at", "asc").
		Build()

	body, statusCode, err := database.DB.Get("budget_collaborators", query)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch collaborators")
	}

	var collaborators []models.Collaborator
	if err := json.Unmarshal(body, &collaborators); err != nil {
		return errInternal(c, "failed to parse collaborators")
	}

	if collaborators == nil {
		collaborators = make([]models.Collaborator, 0)
	}

	// Prepend the budget owner as a synthetic collaborator entry.
	ownerUUID, _ := uuid.Parse(ownerID)
	ownerEntry := models.Collaborator{
		ID:       ownerUUID,
		BudgetID: budgetID,
		UserID:   ownerUUID,
		Role:     "owner",
		AddedAt:  "",
	}
	result := make([]models.Collaborator, 0, 1+len(collaborators))
	result = append(result, ownerEntry)
	result = append(result, collaborators...)

	// Batch-fetch all profiles (owner + collaborators) in a single query.
	userIDs := make([]string, len(result))
	for i, collab := range result {
		userIDs[i] = collab.UserID.String()
	}

	profileQuery := database.NewFilter().
		Select("id,email,full_name,avatar_url,created_at,updated_at").
		In("id", userIDs).
		Build()

	profileBody, profileStatus, profileErr := database.DB.Get("profiles", profileQuery)
	if profileErr == nil && profileStatus == http.StatusOK {
		var profiles []models.Profile
		if err := json.Unmarshal(profileBody, &profiles); err == nil {
			profileMap := make(map[string]*models.Profile, len(profiles))
			for i := range profiles {
				profileMap[profiles[i].ID.String()] = &profiles[i]
			}
			for i := range result {
				if p, ok := profileMap[result[i].UserID.String()]; ok {
					result[i].Profile = p
				}
			}
		}
	}

	return c.JSON(result)
}

// RemoveCollaborator removes a collaborator from a budget. Only the budget
// owner can perform this action.
// On success it broadcasts a collaborator_removed event via WebSocket.
func RemoveCollaborator(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	targetUserID, ok := parseUUIDParam(c, "userId")
	if !ok {
		return errBadRequest(c, "invalid user ID")
	}

	// Only the budget owner can remove collaborators.
	if err := verifyBudgetOwnership(budgetID, userID); err != nil {
		return errForbidden(c, "only the budget owner can remove collaborators")
	}

	// Prevent the owner from removing themselves as a collaborator.
	if targetUserID == userID {
		return errBadRequest(c, "cannot remove yourself as the budget owner")
	}

	// Verify the collaborator exists.
	checkQuery := database.NewFilter().
		Select("id").
		Eq("budget_id", budgetID.String()).
		Eq("user_id", targetUserID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_collaborators", checkQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to verify collaborator")
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return errNotFound(c, "collaborator not found")
	}

	// Delete the collaborator.
	delQuery := database.NewFilter().
		Eq("budget_id", budgetID.String()).
		Eq("user_id", targetUserID.String()).
		Build()

	_, statusCode, err = database.DB.Delete("budget_collaborators", delQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete collaborator")
	}

	broadcast(budgetID.String(), ws.MessageTypeCollabRemoved, map[string]string{
		"user_id": targetUserID.String(),
	})

	return c.SendStatus(fiber.StatusNoContent)
}
