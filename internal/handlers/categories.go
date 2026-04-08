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

// verifySectionOwnership checks that the section belongs to the budget and the user has access.
func verifySectionOwnership(budgetID, sectionID, userID uuid.UUID) error {
	// First verify budget access (owner or collaborator).
	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return err
	}

	// Then verify section belongs to this budget (stored in budget_categories table).
	query := database.NewFilter().
		Select("id").
		Eq("id", sectionID.String()).
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

// CreateCategory creates a new category within a section.
func CreateCategory(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}
	sectionID, err := uuid.Parse(c.Params("sectionId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid section ID"})
	}

	var req models.CreateCategoryRequest
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
	if req.AllocationPercent < 0 || req.AllocationPercent > 100 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "allocation_percent must be between 0 and 100"})
	}

	if err := verifySectionOwnership(budgetID, sectionID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "section not found"})
	}

	now := time.Now().UTC()
	catID := uuid.New()

	cat := models.Category{
		ID:                catID,
		CategoryID:        sectionID,
		Name:              req.Name,
		AllocationPercent: req.AllocationPercent,
		Icon:              req.Icon,
		SortOrder:         req.SortOrder,
		CreatedAt:         now,
	}

	payload := map[string]interface{}{
		"id":                 catID.String(),
		"category_id":        sectionID.String(),
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
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to create category"})
	}

	return c.Status(fiber.StatusCreated).JSON(cat)
}

// UpdateCategory updates an existing category.
func UpdateCategory(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}
	sectionID, err := uuid.Parse(c.Params("sectionId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid section ID"})
	}
	catID, err := uuid.Parse(c.Params("catId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid category ID"})
	}

	var req models.UpdateCategoryRequest
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
	if req.AllocationPercent != nil && (*req.AllocationPercent < 0 || *req.AllocationPercent > 100) {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "allocation_percent must be between 0 and 100"})
	}

	if err := verifySectionOwnership(budgetID, sectionID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "section not found"})
	}

	// Fetch existing category from budget_subcategories table.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", catID.String()).
		Eq("category_id", sectionID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_subcategories", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch category"})
	}

	var cats []models.Category
	if err := json.Unmarshal(body, &cats); err != nil || len(cats) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "category not found"})
	}

	cat := cats[0]

	// Apply partial updates.
	if req.Name != nil {
		cat.Name = *req.Name
	}
	if req.AllocationPercent != nil {
		cat.AllocationPercent = *req.AllocationPercent
	}
	if req.Icon != nil {
		cat.Icon = *req.Icon
	}
	if req.SortOrder != nil {
		cat.SortOrder = *req.SortOrder
	}

	updatePayload := map[string]interface{}{
		"name":               cat.Name,
		"allocation_percent": cat.AllocationPercent,
		"icon":               cat.Icon,
		"sort_order":         cat.SortOrder,
	}
	updateBytes, err := json.Marshal(updatePayload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to serialize request"})
	}

	patchQuery := database.NewFilter().
		Eq("id", catID.String()).
		Eq("category_id", sectionID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budget_subcategories", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to update category"})
	}

	return c.JSON(cat)
}

// DeleteCategory deletes a category and its related expenses.
func DeleteCategory(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}
	sectionID, err := uuid.Parse(c.Params("sectionId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid section ID"})
	}
	catID, err := uuid.Parse(c.Params("catId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid category ID"})
	}

	if err := verifySectionOwnership(budgetID, sectionID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "section not found"})
	}

	// Verify category exists in budget_subcategories table.
	catCheckQuery := database.NewFilter().
		Select("id").
		Eq("id", catID.String()).
		Eq("category_id", sectionID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_subcategories", catCheckQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to verify category"})
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "category not found"})
	}

	// Delete expenses linked to this category.
	expQuery := database.NewFilter().Eq("subcategory_id", catID.String()).Build()
	_, statusCode, err = database.DB.Delete("budget_expenses", expQuery)
	if err != nil || statusCode >= 300 {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete category expenses"})
	}

	// Delete the category from budget_subcategories table.
	delQuery := database.NewFilter().
		Eq("id", catID.String()).
		Eq("category_id", sectionID.String()).
		Build()
	_, statusCode, err = database.DB.Delete("budget_subcategories", delQuery)
	if err != nil || statusCode >= 300 {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete category"})
	}

	return c.SendStatus(fiber.StatusNoContent)
}
