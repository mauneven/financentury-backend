package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// verifyBudgetAccess checks that the user is the budget owner or a collaborator.
func verifyBudgetAccess(budgetID, userID uuid.UUID) error {
	// Check ownership first.
	if err := verifyBudgetOwnership(budgetID, userID); err == nil {
		return nil
	}

	// Check if user is a collaborator.
	query := database.NewFilter().
		Select("id").
		Eq("budget_id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_collaborators", query)
	if err != nil || statusCode != http.StatusOK {
		return fiber.ErrNotFound
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return fiber.ErrNotFound
	}
	return nil
}

// ListCollaborators returns all collaborators for a budget (owner or collaborator).
func ListCollaborators(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}

	// Verify user has access (owner or collaborator).
	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Fetch collaborators.
	query := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("added_at", "asc").
		Build()

	body, statusCode, err := database.DB.Get("budget_collaborators", query)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch collaborators"})
	}

	var collaborators []models.Collaborator
	if err := json.Unmarshal(body, &collaborators); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to parse collaborators"})
	}

	if collaborators == nil {
		collaborators = make([]models.Collaborator, 0)
	}

	// Enrich each collaborator with their profile info.
	for i, collab := range collaborators {
		profileQuery := database.NewFilter().
			Select("id,email,full_name,avatar_url,created_at,updated_at").
			Eq("id", collab.UserID.String()).
			Build()

		profileBody, statusCode, err := database.DB.Get("profiles", profileQuery)
		if err != nil || statusCode != http.StatusOK {
			continue
		}

		var profiles []models.Profile
		if err := json.Unmarshal(profileBody, &profiles); err == nil && len(profiles) > 0 {
			collaborators[i].Profile = &profiles[0]
		}
	}

	return c.JSON(collaborators)
}

// RemoveCollaborator removes a collaborator from a budget (owner only).
func RemoveCollaborator(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}
	targetUserID, err := uuid.Parse(c.Params("userId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid user ID"})
	}

	// Verify the requesting user is the budget owner.
	if err := verifyBudgetOwnership(budgetID, userID); err != nil {
		return c.Status(fiber.StatusForbidden).JSON(models.ErrorResponse{Error: "only the budget owner can remove collaborators"})
	}

	// Verify the collaborator exists.
	checkQuery := database.NewFilter().
		Select("id").
		Eq("budget_id", budgetID.String()).
		Eq("user_id", targetUserID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_collaborators", checkQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to verify collaborator"})
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "collaborator not found"})
	}

	// Delete the collaborator.
	delQuery := database.NewFilter().
		Eq("budget_id", budgetID.String()).
		Eq("user_id", targetUserID.String()).
		Build()

	_, _, _ = database.DB.Delete("budget_collaborators", delQuery)

	return c.SendStatus(fiber.StatusNoContent)
}
