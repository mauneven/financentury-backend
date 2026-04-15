package handlers

import (
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

// CreateCategory creates a new category within a section.
// On success it broadcasts a category_created event via WebSocket.
func CreateCategory(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	sectionID, ok := parseUUIDParam(c, "sectionId")
	if !ok {
		return errBadRequest(c, "invalid section ID")
	}

	var req models.CreateCategoryRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Sanitize text inputs.
	req.Name = strings.TrimSpace(req.Name)
	req.Icon = strings.TrimSpace(req.Icon)

	if req.Name == "" {
		return errBadRequest(c, "name is required")
	}
	if len(req.Name) > maxNameLength {
		return errBadRequest(c, "name too long (max 200 characters)")
	}
	if len(req.Icon) > maxIconLength {
		return errBadRequest(c, "icon too long (max 50 characters)")
	}
	if req.AllocationValue < 0 {
		return errBadRequest(c, "allocation_value must be positive")
	}

	if err := verifySectionOwnership(budgetID, sectionID, userID); err != nil {
		return errNotFound(c, "section not found")
	}

	// Fetch the parent section to get its allocation_value for validation.
	sectionQuery := database.NewFilter().
		Select("allocation_value").
		Eq("id", sectionID.String()).
		Build()
	sectionBody, sectionStatus, sectionErr := database.DB.Get("budget_categories", sectionQuery)
	if sectionErr != nil || sectionStatus != http.StatusOK {
		return errInternal(c, "failed to fetch parent section")
	}
	var parentSections []struct {
		AllocationValue float64 `json:"allocation_value"`
	}
	if err := json.Unmarshal(sectionBody, &parentSections); err != nil || len(parentSections) == 0 {
		return errInternal(c, "failed to parse parent section")
	}
	sectionAlloc := parentSections[0].AllocationValue

	// Validate that total category allocation won't exceed section's value.
	existingCatQuery := database.NewFilter().
		Select("allocation_value").
		Eq("category_id", sectionID.String()).
		Build()

	existingCatBody, existingCatStatus, existingCatErr := database.DB.Get("budget_subcategories", existingCatQuery)
	if existingCatErr != nil || existingCatStatus != http.StatusOK {
		return errInternal(c, "failed to check existing category allocations")
	}

	var existingCats []struct {
		AllocationValue float64 `json:"allocation_value"`
	}
	if err := json.Unmarshal(existingCatBody, &existingCats); err != nil {
		return errInternal(c, "failed to parse existing category allocations")
	}

	var totalCatAllocation float64
	for _, ec := range existingCats {
		totalCatAllocation += ec.AllocationValue
	}
	if totalCatAllocation+req.AllocationValue > sectionAlloc {
		return errBadRequest(c, "total category allocation would exceed section budget")
	}

	now := time.Now().UTC()
	catID := uuid.New()

	cat := models.Category{
		ID:                catID,
		CategoryID:        sectionID,
		Name:              req.Name,
		AllocationValue: req.AllocationValue,
		Icon:              req.Icon,
		SortOrder:         req.SortOrder,
		CreatedAt:         now,
	}

	payload := map[string]interface{}{
		"id":                 catID.String(),
		"category_id":        sectionID.String(),
		"name":               req.Name,
		"allocation_value": req.AllocationValue,
		"icon":               req.Icon,
		"sort_order":         req.SortOrder,
		"created_at":         now.Format(time.RFC3339Nano),
	}
	payloadBytes, err := marshalJSON(payload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	_, statusCode, err := database.DB.Post("budget_subcategories", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		return errInternal(c, "failed to create category")
	}

	broadcast(budgetID.String(), ws.MessageTypeCategoryCreated, cat)
	broadcastToLinkedTargets(budgetID, ws.MessageTypeCategoryCreated, cat)

	return c.Status(fiber.StatusCreated).JSON(cat)
}

// UpdateCategory updates an existing category.
// On success it broadcasts a category_updated event via WebSocket.
func UpdateCategory(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	sectionID, ok := parseUUIDParam(c, "sectionId")
	if !ok {
		return errBadRequest(c, "invalid section ID")
	}

	catID, ok := parseUUIDParam(c, "catId")
	if !ok {
		return errBadRequest(c, "invalid category ID")
	}

	var req models.UpdateCategoryRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Sanitize text inputs.
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
	}
	if req.Icon != nil {
		trimmed := strings.TrimSpace(*req.Icon)
		req.Icon = &trimmed
	}

	// Validate optional fields.
	if req.Name != nil && *req.Name == "" {
		return errBadRequest(c, "name cannot be empty")
	}
	if req.Name != nil && len(*req.Name) > maxNameLength {
		return errBadRequest(c, "name too long (max 200 characters)")
	}
	if req.Icon != nil && len(*req.Icon) > maxIconLength {
		return errBadRequest(c, "icon too long (max 50 characters)")
	}
	if req.AllocationValue != nil && *req.AllocationValue < 0 {
		return errBadRequest(c, "allocation_value must be positive")
	}

	if err := verifySectionOwnership(budgetID, sectionID, userID); err != nil {
		return errNotFound(c, "section not found")
	}

	// Fetch existing category from the categories table.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", catID.String()).
		Eq("category_id", sectionID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_subcategories", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch category")
	}

	var cats []models.Category
	if err := json.Unmarshal(body, &cats); err != nil || len(cats) == 0 {
		return errNotFound(c, "category not found")
	}

	cat := cats[0]

	// Validate that updated total category allocation won't exceed section's value.
	if req.AllocationValue != nil {
		// Fetch parent section's allocation_value.
		secQuery := database.NewFilter().
			Select("allocation_value").
			Eq("id", sectionID.String()).
			Build()
		secBody, secStatus, secErr := database.DB.Get("budget_categories", secQuery)
		if secErr != nil || secStatus != http.StatusOK {
			return errInternal(c, "failed to fetch parent section")
		}
		var parentSecs []struct {
			AllocationValue float64 `json:"allocation_value"`
		}
		if err := json.Unmarshal(secBody, &parentSecs); err != nil || len(parentSecs) == 0 {
			return errInternal(c, "failed to parse parent section")
		}
		sectionAlloc := parentSecs[0].AllocationValue

		allCatQuery := database.NewFilter().
			Select("id,allocation_value").
			Eq("category_id", sectionID.String()).
			Build()

		allCatBody, allCatStatus, allCatErr := database.DB.Get("budget_subcategories", allCatQuery)
		if allCatErr != nil || allCatStatus != http.StatusOK {
			return errInternal(c, "failed to check existing category allocations")
		}

		var allCats []struct {
			ID              string  `json:"id"`
			AllocationValue float64 `json:"allocation_value"`
		}
		if err := json.Unmarshal(allCatBody, &allCats); err != nil {
			return errInternal(c, "failed to parse existing category allocations")
		}

		var totalCatAlloc float64
		for _, ac := range allCats {
			if ac.ID == catID.String() {
				continue // exclude the category being updated
			}
			totalCatAlloc += ac.AllocationValue
		}
		if totalCatAlloc+*req.AllocationValue > sectionAlloc {
			return errBadRequest(c, "total category allocation would exceed section budget")
		}
	}

	// Apply partial updates.
	if req.Name != nil {
		cat.Name = *req.Name
	}
	if req.AllocationValue != nil {
		cat.AllocationValue = *req.AllocationValue
	}
	if req.Icon != nil {
		cat.Icon = *req.Icon
	}
	if req.SortOrder != nil {
		cat.SortOrder = *req.SortOrder
	}

	updatePayload := map[string]interface{}{
		"name":               cat.Name,
		"allocation_value": cat.AllocationValue,
		"icon":               cat.Icon,
		"sort_order":         cat.SortOrder,
	}
	updateBytes, err := marshalJSON(updatePayload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	patchQuery := database.NewFilter().
		Eq("id", catID.String()).
		Eq("category_id", sectionID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budget_subcategories", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to update category")
	}

	broadcast(budgetID.String(), ws.MessageTypeCategoryUpdated, cat)
	broadcastToLinkedTargets(budgetID, ws.MessageTypeCategoryUpdated, cat)

	return c.JSON(cat)
}

// DeleteCategory deletes a category and its related expenses.
// On success it broadcasts a category_deleted event via WebSocket.
func DeleteCategory(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	sectionID, ok := parseUUIDParam(c, "sectionId")
	if !ok {
		return errBadRequest(c, "invalid section ID")
	}

	catID, ok := parseUUIDParam(c, "catId")
	if !ok {
		return errBadRequest(c, "invalid category ID")
	}

	if err := verifySectionOwnership(budgetID, sectionID, userID); err != nil {
		return errNotFound(c, "section not found")
	}

	cid := catID.String()

	// Verify category exists in budget_subcategories table.
	catCheckQuery := database.NewFilter().
		Select("id").
		Eq("id", cid).
		Eq("category_id", sectionID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_subcategories", catCheckQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to verify category")
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return errNotFound(c, "category not found")
	}

	// Delete expenses linked to this category.
	expQuery := database.NewFilter().Eq("subcategory_id", cid).Build()
	_, statusCode, err = database.DB.Delete("budget_expenses", expQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete category expenses")
	}

	// Delete the category.
	delQuery := database.NewFilter().
		Eq("id", cid).
		Eq("category_id", sectionID.String()).
		Build()
	_, statusCode, err = database.DB.Delete("budget_subcategories", delQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete category")
	}

	broadcast(budgetID.String(), ws.MessageTypeCategoryDeleted, map[string]string{"id": cid})
	broadcastToLinkedTargets(budgetID, ws.MessageTypeCategoryDeleted, map[string]string{"id": cid})

	return c.SendStatus(fiber.StatusNoContent)
}
