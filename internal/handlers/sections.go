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

// SectionWithCategories embeds a Section and its child Categories for the
// ListSections response.
type SectionWithCategories struct {
	models.Section
	Categories []models.Category `json:"categories"`
}

// ListSections returns all sections (each with its categories) for a budget.
//
// Categories are fetched in a single batched query using an IN filter on
// section IDs, replacing the previous N+1 pattern that issued one query per
// section.
func ListSections(c *fiber.Ctx) error {
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

	// Fetch sections from budget_categories table.
	sectionQuery := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("sort_order", "asc").
		Build()

	sectionBody, statusCode, err := database.DB.Get("budget_categories", sectionQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch sections")
	}

	var sections []models.Section
	if err := json.Unmarshal(sectionBody, &sections); err != nil {
		return errInternal(c, "failed to parse sections")
	}

	if sections == nil {
		sections = make([]models.Section, 0)
	}

	// Batch-fetch all categories for every section in one query instead of
	// issuing one query per section (N+1 elimination).
	catsBySection := make(map[uuid.UUID][]models.Category, len(sections))

	if len(sections) > 0 {
		sectionIDs := make([]string, len(sections))
		for i, s := range sections {
			sectionIDs[i] = s.ID.String()
		}

		catQuery := database.NewFilter().
			Select("*").
			In("category_id", sectionIDs).
			Order("sort_order", "asc").
			Build()

		catBody, catStatus, catErr := database.DB.Get("budget_subcategories", catQuery)
		if catErr != nil || catStatus != http.StatusOK {
			return errInternal(c, "failed to fetch categories")
		}

		var allCats []models.Category
		if err := json.Unmarshal(catBody, &allCats); err != nil {
			return errInternal(c, "failed to parse categories")
		}

		for _, cat := range allCats {
			catsBySection[cat.CategoryID] = append(catsBySection[cat.CategoryID], cat)
		}
	}

	// Build response with categories grouped under their parent section.
	result := make([]SectionWithCategories, 0, len(sections))
	for _, section := range sections {
		cats := catsBySection[section.ID]
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
// On success it broadcasts a section_created event via WebSocket.
func CreateSection(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	var req models.CreateSectionRequest
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
	if req.AllocationPercent < 0 || req.AllocationPercent > 100 {
		return errBadRequest(c, "allocation_percent must be between 0 and 100")
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Validate that total allocation across all sections won't exceed 100%.
	existingQuery := database.NewFilter().
		Select("allocation_percent").
		Eq("budget_id", budgetID.String()).
		Build()

	existingBody, existingStatus, existingErr := database.DB.Get("budget_categories", existingQuery)
	if existingErr != nil || existingStatus != http.StatusOK {
		return errInternal(c, "failed to check existing allocations")
	}

	var existingSections []struct {
		AllocationPercent float64 `json:"allocation_percent"`
	}
	if err := json.Unmarshal(existingBody, &existingSections); err != nil {
		return errInternal(c, "failed to parse existing allocations")
	}

	var totalAllocation float64
	for _, s := range existingSections {
		totalAllocation += s.AllocationPercent
	}
	if totalAllocation+req.AllocationPercent > 100 {
		return errBadRequest(c, "total allocation would exceed 100%")
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
	payloadBytes, err := marshalJSON(payload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	_, statusCode, err := database.DB.Post("budget_categories", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		return errInternal(c, "failed to create section")
	}

	broadcast(budgetID.String(), ws.MessageTypeSectionCreated, section)

	return c.Status(fiber.StatusCreated).JSON(section)
}

// UpdateSection updates an existing section.
// On success it broadcasts a section_updated event via WebSocket.
func UpdateSection(c *fiber.Ctx) error {
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

	var req models.UpdateSectionRequest
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
	if req.AllocationPercent != nil && (*req.AllocationPercent < 0 || *req.AllocationPercent > 100) {
		return errBadRequest(c, "allocation_percent must be between 0 and 100")
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Fetch existing section from budget_categories table.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", sectionID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_categories", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch section")
	}

	var sections []models.Section
	if err := json.Unmarshal(body, &sections); err != nil || len(sections) == 0 {
		return errNotFound(c, "section not found")
	}

	section := sections[0]

	// Validate that updated total allocation across all sections won't exceed 100%.
	if req.AllocationPercent != nil {
		allQuery := database.NewFilter().
			Select("id,allocation_percent").
			Eq("budget_id", budgetID.String()).
			Build()

		allBody, allStatus, allErr := database.DB.Get("budget_categories", allQuery)
		if allErr != nil || allStatus != http.StatusOK {
			return errInternal(c, "failed to check existing allocations")
		}

		var allSections []struct {
			ID                string  `json:"id"`
			AllocationPercent float64 `json:"allocation_percent"`
		}
		if err := json.Unmarshal(allBody, &allSections); err != nil {
			return errInternal(c, "failed to parse existing allocations")
		}

		var totalAllocation float64
		for _, s := range allSections {
			if s.ID == sectionID.String() {
				continue // exclude the section being updated
			}
			totalAllocation += s.AllocationPercent
		}
		if totalAllocation+*req.AllocationPercent > 100 {
			return errBadRequest(c, "total allocation would exceed 100%")
		}
	}

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
	updateBytes, err := marshalJSON(updatePayload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	patchQuery := database.NewFilter().
		Eq("id", sectionID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budget_categories", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to update section")
	}

	broadcast(budgetID.String(), ws.MessageTypeSectionUpdated, section)

	return c.JSON(section)
}

// DeleteSection deletes a section and its categories (and related expenses).
// On success it broadcasts a section_deleted event via WebSocket.
func DeleteSection(c *fiber.Ctx) error {
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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Verify section exists in budget_categories table.
	sectionCheckQuery := database.NewFilter().
		Select("id").
		Eq("id", sectionID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_categories", sectionCheckQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to verify section")
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return errNotFound(c, "section not found")
	}

	sid := sectionID.String()

	// Get category IDs for this section to delete related expenses.
	catQuery := database.NewFilter().Select("id").Eq("category_id", sid).Build()
	catBody, catStatusCode, catErr := database.DB.Get("budget_subcategories", catQuery)

	if catErr == nil && catStatusCode == http.StatusOK {
		var cats []struct{ ID string `json:"id"` }
		if err := json.Unmarshal(catBody, &cats); err == nil && len(cats) > 0 {
			catIDs := make([]string, len(cats))
			for i, ct := range cats {
				catIDs[i] = ct.ID
			}
			expQuery := database.NewFilter().In("subcategory_id", catIDs).Build()
			_, statusCode, err := database.DB.Delete("budget_expenses", expQuery)
			if err != nil || statusCode >= 300 {
				return errInternal(c, "failed to delete section expenses")
			}
		}
	}

	// Delete categories belonging to this section.
	delCatQuery := database.NewFilter().Eq("category_id", sid).Build()
	_, statusCode, err = database.DB.Delete("budget_subcategories", delCatQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete categories")
	}

	// Delete the section.
	delSectionQuery := database.NewFilter().
		Eq("id", sid).
		Eq("budget_id", budgetID.String()).
		Build()
	_, statusCode, err = database.DB.Delete("budget_categories", delSectionQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete section")
	}

	broadcast(budgetID.String(), ws.MessageTypeSectionDeleted, map[string]string{"id": sid})

	return c.SendStatus(fiber.StatusNoContent)
}
