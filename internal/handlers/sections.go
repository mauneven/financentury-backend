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

// ListSections returns all sections (each with its categories) for a budget.
func ListSections(c *fiber.Ctx) error {
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

	// Fetch sections from budget_categories table.
	sectionQuery := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("sort_order", "asc").
		Build()

	sectionBody, statusCode, err := database.DB.Get("budget_categories", sectionQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch sections"})
	}

	var sections []models.Section
	if err := json.Unmarshal(sectionBody, &sections); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to parse sections"})
	}

	if sections == nil {
		sections = make([]models.Section, 0)
	}

	// Build response with categories per section.
	type SectionWithCategories struct {
		models.Section
		Categories []models.Category `json:"categories"`
	}

	result := make([]SectionWithCategories, 0, len(sections))
	for _, section := range sections {
		catQuery := database.NewFilter().
			Select("*").
			Eq("category_id", section.ID.String()).
			Order("sort_order", "asc").
			Build()

		catBody, statusCode, err := database.DB.Get("budget_subcategories", catQuery)
		if err != nil || statusCode != http.StatusOK {
			return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch categories"})
		}

		var cats []models.Category
		if err := json.Unmarshal(catBody, &cats); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to parse categories"})
		}

		if cats == nil {
			cats = make([]models.Category, 0)
		}

		result = append(result, SectionWithCategories{
			Section:    section,
			Categories: cats,
		})
	}

	return c.JSON(result)
}

// CreateSection creates a new section for a budget.
func CreateSection(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}

	var req models.CreateSectionRequest
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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	now := time.Now().UTC()
	sectionID := uuid.New()

	section := models.Section{
		ID:                sectionID,
		BudgetID:          budgetID,
		Name:              req.Name,
		AllocationPercent: req.AllocationPercent,
		Icon:              req.Icon,
		SortOrder:         req.SortOrder,
		CreatedAt:         now,
	}

	payload := map[string]interface{}{
		"id":                 sectionID.String(),
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
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to create section"})
	}

	return c.Status(fiber.StatusCreated).JSON(section)
}

// UpdateSection updates an existing section.
func UpdateSection(c *fiber.Ctx) error {
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

	var req models.UpdateSectionRequest
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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Fetch existing section from budget_categories table.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", sectionID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_categories", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch section"})
	}

	var sections []models.Section
	if err := json.Unmarshal(body, &sections); err != nil || len(sections) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "section not found"})
	}

	section := sections[0]

	// Apply partial updates.
	if req.Name != nil {
		section.Name = *req.Name
	}
	if req.AllocationPercent != nil {
		section.AllocationPercent = *req.AllocationPercent
	}
	if req.Icon != nil {
		section.Icon = *req.Icon
	}
	if req.SortOrder != nil {
		section.SortOrder = *req.SortOrder
	}

	updatePayload := map[string]interface{}{
		"name":               section.Name,
		"allocation_percent": section.AllocationPercent,
		"icon":               section.Icon,
		"sort_order":         section.SortOrder,
	}
	updateBytes, err := json.Marshal(updatePayload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to serialize request"})
	}

	patchQuery := database.NewFilter().
		Eq("id", sectionID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budget_categories", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to update section"})
	}

	return c.JSON(section)
}

// DeleteSection deletes a section and its categories.
func DeleteSection(c *fiber.Ctx) error {
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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Verify section exists in budget_categories table.
	sectionCheckQuery := database.NewFilter().
		Select("id").
		Eq("id", sectionID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_categories", sectionCheckQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to verify section"})
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "section not found"})
	}

	// Get category IDs for this section to delete related expenses.
	catQuery := database.NewFilter().Select("id").Eq("category_id", sectionID.String()).Build()
	catBody, catStatusCode, catErr := database.DB.Get("budget_subcategories", catQuery)

	if catErr == nil && catStatusCode == http.StatusOK {
		var cats []struct{ ID string `json:"id"` }
		if err := json.Unmarshal(catBody, &cats); err == nil && len(cats) > 0 {
			catIDs := make([]string, len(cats))
			for i, ct := range cats {
				catIDs[i] = ct.ID
			}
			// Delete expenses linked to these categories.
			expQuery := database.NewFilter().In("subcategory_id", catIDs).Build()
			_, statusCode, err := database.DB.Delete("budget_expenses", expQuery)
			if err != nil || statusCode >= 300 {
				return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete section expenses"})
			}
		}
	}

	// Delete categories belonging to this section (from budget_subcategories table).
	delCatQuery := database.NewFilter().Eq("category_id", sectionID.String()).Build()
	_, statusCode, err = database.DB.Delete("budget_subcategories", delCatQuery)
	if err != nil || statusCode >= 300 {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete categories"})
	}

	// Delete the section from budget_categories table.
	delSectionQuery := database.NewFilter().
		Eq("id", sectionID.String()).
		Eq("budget_id", budgetID.String()).
		Build()
	_, statusCode, err = database.DB.Delete("budget_categories", delSectionQuery)
	if err != nil || statusCode >= 300 {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete section"})
	}

	return c.SendStatus(fiber.StatusNoContent)
}
