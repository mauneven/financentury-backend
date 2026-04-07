package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// verifyCategoryOwnership checks that the category belongs to the budget and the user owns the budget.
func verifyCategoryOwnership(budgetID, catID, userID uuid.UUID) error {
	// First verify budget access (owner or collaborator).
	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return err
	}

	// Then verify category belongs to this budget.
	query := database.NewFilter().
		Select("id").
		Eq("id", catID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_categories", query)
	if err != nil || statusCode != http.StatusOK {
		return fiber.ErrNotFound
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return fiber.ErrNotFound
	}
	return nil
}

// CreateSubcategory creates a new subcategory within a category.
func CreateSubcategory(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}
	catID, err := uuid.Parse(c.Params("catId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid category ID"})
	}

	var req models.CreateSubcategoryRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid request body"})
	}

	if req.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "name is required"})
	}
	if len(req.Name) > maxNameLength {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "name too long (max 200 characters)"})
	}
	if len(req.Icon) > maxIconLength {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "icon too long (max 50 characters)"})
	}

	if err := verifyCategoryOwnership(budgetID, catID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "category not found"})
	}

	now := time.Now().UTC()
	subID := uuid.New()

	sub := models.Subcategory{
		ID:                subID,
		CategoryID:        catID,
		Name:              req.Name,
		AllocationPercent: req.AllocationPercent,
		Icon:              req.Icon,
		SortOrder:         req.SortOrder,
		CreatedAt:         now,
	}

	payload := map[string]interface{}{
		"id":                 subID.String(),
		"category_id":        catID.String(),
		"name":               req.Name,
		"allocation_percent": req.AllocationPercent,
		"icon":               req.Icon,
		"sort_order":         req.SortOrder,
		"created_at":         now.Format(time.RFC3339Nano),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to serialize request"})
	}

	_, statusCode, err := database.DB.Post("budget_subcategories", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to create subcategory"})
	}

	return c.Status(fiber.StatusCreated).JSON(sub)
}

// UpdateSubcategory updates an existing subcategory.
func UpdateSubcategory(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}
	catID, err := uuid.Parse(c.Params("catId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid category ID"})
	}
	subID, err := uuid.Parse(c.Params("subId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid subcategory ID"})
	}

	var req models.UpdateSubcategoryRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid request body"})
	}

	if req.Name != nil && *req.Name == "" {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "name cannot be empty"})
	}
	if req.Name != nil && len(*req.Name) > maxNameLength {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "name too long (max 200 characters)"})
	}
	if req.Icon != nil && len(*req.Icon) > maxIconLength {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "icon too long (max 50 characters)"})
	}

	if err := verifyCategoryOwnership(budgetID, catID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "category not found"})
	}

	// Fetch existing subcategory.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", subID.String()).
		Eq("category_id", catID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_subcategories", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch subcategory"})
	}

	var subs []models.Subcategory
	if err := json.Unmarshal(body, &subs); err != nil || len(subs) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "subcategory not found"})
	}

	sub := subs[0]

	// Apply partial updates.
	if req.Name != nil {
		sub.Name = *req.Name
	}
	if req.AllocationPercent != nil {
		sub.AllocationPercent = *req.AllocationPercent
	}
	if req.Icon != nil {
		sub.Icon = *req.Icon
	}
	if req.SortOrder != nil {
		sub.SortOrder = *req.SortOrder
	}

	updatePayload := map[string]interface{}{
		"name":               sub.Name,
		"allocation_percent": sub.AllocationPercent,
		"icon":               sub.Icon,
		"sort_order":         sub.SortOrder,
	}
	updateBytes, err := json.Marshal(updatePayload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to serialize request"})
	}

	patchQuery := database.NewFilter().
		Eq("id", subID.String()).
		Eq("category_id", catID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budget_subcategories", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to update subcategory"})
	}

	return c.JSON(sub)
}

// DeleteSubcategory deletes a subcategory and its related expenses.
func DeleteSubcategory(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}
	catID, err := uuid.Parse(c.Params("catId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid category ID"})
	}
	subID, err := uuid.Parse(c.Params("subId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid subcategory ID"})
	}

	if err := verifyCategoryOwnership(budgetID, catID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "category not found"})
	}

	// Verify subcategory exists.
	subCheckQuery := database.NewFilter().
		Select("id").
		Eq("id", subID.String()).
		Eq("category_id", catID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_subcategories", subCheckQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to verify subcategory"})
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "subcategory not found"})
	}

	// Delete expenses linked to this subcategory.
	expQuery := database.NewFilter().Eq("subcategory_id", subID.String()).Build()
	_, statusCode, err = database.DB.Delete("budget_expenses", expQuery)
	if err != nil || statusCode >= 300 {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete subcategory expenses"})
	}

	// Delete the subcategory.
	delQuery := database.NewFilter().
		Eq("id", subID.String()).
		Eq("category_id", catID.String()).
		Build()
	_, statusCode, err = database.DB.Delete("budget_subcategories", delQuery)
	if err != nil || statusCode >= 300 {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete subcategory"})
	}

	return c.SendStatus(fiber.StatusNoContent)
}
