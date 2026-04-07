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

// verifyBudgetOwnership checks that the authenticated user owns the budget via Supabase REST.
func verifyBudgetOwnership(budgetID, userID uuid.UUID) error {
	query := database.NewFilter().
		Select("id").
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budgets", query)
	if err != nil || statusCode != http.StatusOK {
		return fiber.ErrNotFound
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return fiber.ErrNotFound
	}
	return nil
}

// ListCategories returns all categories (with subcategories) for a budget.
func ListCategories(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Fetch categories.
	catQuery := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("sort_order", "asc").
		Build()

	catBody, statusCode, err := database.DB.Get("budget_categories", catQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch categories"})
	}

	var categories []models.Category
	if err := json.Unmarshal(catBody, &categories); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to parse categories"})
	}

	if categories == nil {
		categories = make([]models.Category, 0)
	}

	// Build response with subcategories per category.
	type CategoryWithSubs struct {
		models.Category
		Subcategories []models.Subcategory `json:"subcategories"`
	}

	result := make([]CategoryWithSubs, 0, len(categories))
	for _, cat := range categories {
		subQuery := database.NewFilter().
			Select("*").
			Eq("category_id", cat.ID.String()).
			Order("sort_order", "asc").
			Build()

		subBody, statusCode, err := database.DB.Get("budget_subcategories", subQuery)
		if err != nil || statusCode != http.StatusOK {
			return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch subcategories"})
		}

		var subs []models.Subcategory
		if err := json.Unmarshal(subBody, &subs); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to parse subcategories"})
		}

		if subs == nil {
			subs = make([]models.Subcategory, 0)
		}

		result = append(result, CategoryWithSubs{
			Category:      cat,
			Subcategories: subs,
		})
	}

	return c.JSON(result)
}

// CreateCategory creates a new category for a budget.
func CreateCategory(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	now := time.Now().UTC()
	catID := uuid.New()

	cat := models.Category{
		ID:                catID,
		BudgetID:          budgetID,
		Name:              req.Name,
		AllocationPercent: req.AllocationPercent,
		Icon:              req.Icon,
		SortOrder:         req.SortOrder,
		CreatedAt:         now,
	}

	payload := map[string]interface{}{
		"id":                 catID.String(),
		"budget_id":          budgetID.String(),
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

	_, statusCode, err := database.DB.Post("budget_categories", payloadBytes)
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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Fetch existing category.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", catID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_categories", getQuery)
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
		Eq("budget_id", budgetID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budget_categories", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to update category"})
	}

	return c.JSON(cat)
}

// DeleteCategory deletes a category and its subcategories.
func DeleteCategory(c *fiber.Ctx) error {
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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Verify category exists.
	catCheckQuery := database.NewFilter().
		Select("id").
		Eq("id", catID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_categories", catCheckQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to verify category"})
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "category not found"})
	}

	// Get subcategory IDs for this category to delete related expenses.
	subQuery := database.NewFilter().Select("id").Eq("category_id", catID.String()).Build()
	subBody, subStatusCode, subErr := database.DB.Get("budget_subcategories", subQuery)

	if subErr == nil && subStatusCode == http.StatusOK {
		var subs []struct{ ID string `json:"id"` }
		if err := json.Unmarshal(subBody, &subs); err == nil && len(subs) > 0 {
			subIDs := make([]string, len(subs))
			for i, s := range subs {
				subIDs[i] = s.ID
			}
			// Delete expenses linked to these subcategories.
			expQuery := database.NewFilter().In("subcategory_id", subIDs).Build()
			_, statusCode, err := database.DB.Delete("budget_expenses", expQuery)
			if err != nil || statusCode >= 300 {
				return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete category expenses"})
			}
		}
	}

	// Delete subcategories.
	delSubQuery := database.NewFilter().Eq("category_id", catID.String()).Build()
	_, statusCode, err = database.DB.Delete("budget_subcategories", delSubQuery)
	if err != nil || statusCode >= 300 {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete subcategories"})
	}

	// Delete the category.
	delCatQuery := database.NewFilter().
		Eq("id", catID.String()).
		Eq("budget_id", budgetID.String()).
		Build()
	_, statusCode, err = database.DB.Delete("budget_categories", delCatQuery)
	if err != nil || statusCode >= 300 {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete category"})
	}

	return c.SendStatus(fiber.StatusNoContent)
}
