package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// guidedSection defines a section for the 50/30/20 guided mode.
type guidedSection struct {
	Name       string
	Percent    float64
	Icon       string
	SortOrder  int
	Categories []guidedCategory
}

// guidedCategory defines a category within a guided section.
type guidedCategory struct {
	Name      string
	Percent   float64
	Icon      string
	SortOrder int
}

// getGuidedSections returns the 50/30/20 guided template used to seed new
// guided-mode budgets.
func getGuidedSections() []guidedSection {
	return []guidedSection{
		{
			Name: "Necesidades", Percent: 50, Icon: "🏠", SortOrder: 1,
			Categories: []guidedCategory{
				{Name: "Vivienda", Percent: 28, Icon: "🏠", SortOrder: 1},
				{Name: "Comida", Percent: 12, Icon: "🍽️", SortOrder: 2},
				{Name: "Transporte", Percent: 6, Icon: "🚗", SortOrder: 3},
				{Name: "Servicios", Percent: 4, Icon: "💡", SortOrder: 4},
			},
		},
		{
			Name: "Deseos", Percent: 30, Icon: "✨", SortOrder: 2,
			Categories: []guidedCategory{
				{Name: "Salidas", Percent: 10, Icon: "🎉", SortOrder: 1},
				{Name: "Entretenimiento", Percent: 5, Icon: "🎬", SortOrder: 2},
				{Name: "Ropa", Percent: 7, Icon: "👕", SortOrder: 3},
				{Name: "Viajes", Percent: 8, Icon: "✈️", SortOrder: 4},
			},
		},
		{
			Name: "Ahorro", Percent: 20, Icon: "💰", SortOrder: 3,
			Categories: []guidedCategory{
				{Name: "Fondo de emergencia", Percent: 8, Icon: "🏦", SortOrder: 1},
				{Name: "Inversión", Percent: 12, Icon: "📈", SortOrder: 2},
			},
		},
	}
}

// ListBudgets returns all budgets for the authenticated user (owned + collaborative).
func ListBudgets(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	// Fetch owned budgets.
	query := database.NewFilter().
		Select("*").
		Eq("user_id", userID.String()).
		Order("created_at", "desc").
		Build()

	body, statusCode, err := database.DB.Get("budgets", query)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch budgets")
	}

	var budgets []models.Budget
	if err := json.Unmarshal(body, &budgets); err != nil {
		return errInternal(c, "failed to parse budgets")
	}

	if budgets == nil {
		budgets = make([]models.Budget, 0)
	}

	// Fetch budgets where user is a collaborator.
	collabQuery := database.NewFilter().
		Select("budget_id").
		Eq("user_id", userID.String()).
		Build()

	collabBody, collabStatus, collabErr := database.DB.Get("budget_collaborators", collabQuery)
	if collabErr == nil && collabStatus == http.StatusOK {
		var collabs []struct {
			BudgetID string `json:"budget_id"`
		}
		if err := json.Unmarshal(collabBody, &collabs); err == nil && len(collabs) > 0 {
			budgetIDs := make([]string, len(collabs))
			for i, cb := range collabs {
				budgetIDs[i] = cb.BudgetID
			}

			collabBudgetQuery := database.NewFilter().
				Select("*").
				In("id", budgetIDs).
				Order("created_at", "desc").
				Build()

			collabBudgetBody, collabBudgetStatus, collabBudgetErr := database.DB.Get("budgets", collabBudgetQuery)
			if collabBudgetErr == nil && collabBudgetStatus == http.StatusOK {
				var collabBudgets []models.Budget
				if err := json.Unmarshal(collabBudgetBody, &collabBudgets); err == nil {
					budgets = append(budgets, collabBudgets...)
				}
			}
		}
	}

	return c.JSON(budgets)
}

// CreateBudget creates a new budget and optionally seeds guided sections.
// On success it broadcasts a budget_created event via WebSocket.
func CreateBudget(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	var req models.CreateBudgetRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Validate required fields.
	if req.Name == "" {
		return errBadRequest(c, "name is required")
	}
	if len(req.Name) > maxNameLength {
		return errBadRequest(c, "name too long (max 200 characters)")
	}
	if req.MonthlyIncome <= 0 {
		return errBadRequest(c, "monthly_income must be positive")
	}
	if req.MonthlyIncome > maxAmountValue {
		return errBadRequest(c, "monthly_income exceeds maximum allowed value")
	}

	// Apply defaults.
	if req.Currency == "" {
		req.Currency = "COP"
	}
	if req.BillingPeriodMonths <= 0 {
		req.BillingPeriodMonths = 1
	}
	if req.Mode == "" {
		req.Mode = "manual"
	}

	// Validate mode and currency.
	if !validBudgetModes[req.Mode] {
		return errBadRequest(c, "invalid mode, must be 'manual' or 'guided'")
	}
	if len(req.Currency) != maxCurrencyLength {
		return errBadRequest(c, "invalid currency code")
	}

	now := time.Now().UTC()
	budgetID := uuid.New()

	budget := models.Budget{
		ID:                  budgetID,
		UserID:              userID,
		Name:                req.Name,
		MonthlyIncome:       req.MonthlyIncome,
		Currency:            req.Currency,
		BillingPeriodMonths: req.BillingPeriodMonths,
		Mode:                req.Mode,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	budgetPayload := map[string]interface{}{
		"id":                    budget.ID.String(),
		"user_id":               budget.UserID.String(),
		"name":                  budget.Name,
		"monthly_income":        budget.MonthlyIncome,
		"currency":              budget.Currency,
		"billing_period_months": budget.BillingPeriodMonths,
		"mode":                  budget.Mode,
		"created_at":            now.Format(time.RFC3339Nano),
		"updated_at":            now.Format(time.RFC3339Nano),
	}

	payloadBytes, err := marshalJSON(budgetPayload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	_, statusCode, err := database.DB.Post("budgets", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		return errInternal(c, "failed to create budget")
	}

	// Seed guided sections if mode is "guided".
	if budget.Mode == "guided" {
		if err := seedGuidedSections(budget.ID, now); err != nil {
			return errInternal(c, "failed to create guided sections")
		}
	}

	broadcast(budgetID.String(), ws.MessageTypeBudgetCreated, budget)

	return c.Status(fiber.StatusCreated).JSON(budget)
}

// seedGuidedSections creates the 50/30/20 template sections and categories.
func seedGuidedSections(budgetID uuid.UUID, now time.Time) error {
	for _, gs := range getGuidedSections() {
		sectionID := uuid.New()
		sectionPayload := map[string]interface{}{
			"id":                 sectionID.String(),
			"budget_id":          budgetID.String(),
			"name":               gs.Name,
			"allocation_percent": gs.Percent,
			"icon":               gs.Icon,
			"sort_order":         gs.SortOrder,
			"created_at":         now.Format(time.RFC3339Nano),
		}
		sectionBytes, err := marshalJSON(sectionPayload)
		if err != nil {
			return err
		}
		_, statusCode, err := database.DB.Post("budget_categories", sectionBytes)
		if err != nil || statusCode != http.StatusCreated {
			return fiber.ErrInternalServerError
		}

		for _, gc := range gs.Categories {
			catID := uuid.New()
			catPayload := map[string]interface{}{
				"id":                 catID.String(),
				"category_id":        sectionID.String(),
				"name":               gc.Name,
				"allocation_percent": gc.Percent,
				"icon":               gc.Icon,
				"sort_order":         gc.SortOrder,
				"created_at":         now.Format(time.RFC3339Nano),
			}
			catBytes, err := marshalJSON(catPayload)
			if err != nil {
				return err
			}
			_, statusCode, err := database.DB.Post("budget_subcategories", catBytes)
			if err != nil || statusCode != http.StatusCreated {
				return fiber.ErrInternalServerError
			}
		}
	}
	return nil
}

// GetBudget returns a single budget by ID (owner or collaborator).
func GetBudget(c *fiber.Ctx) error {
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

	query := database.NewFilter().
		Select("*").
		Eq("id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budgets", query)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch budget")
	}

	var budgets []models.Budget
	if err := json.Unmarshal(body, &budgets); err != nil || len(budgets) == 0 {
		return errNotFound(c, "budget not found")
	}

	return c.JSON(budgets[0])
}

// UpdateBudget updates an existing budget. Only the owner can update.
// On success it broadcasts a budget_updated event via WebSocket.
func UpdateBudget(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	var req models.UpdateBudgetRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Validate optional fields.
	if req.Name != nil && *req.Name == "" {
		return errBadRequest(c, "name cannot be empty")
	}
	if req.Name != nil && len(*req.Name) > maxNameLength {
		return errBadRequest(c, "name too long (max 200 characters)")
	}
	if req.MonthlyIncome != nil && *req.MonthlyIncome <= 0 {
		return errBadRequest(c, "monthly_income must be positive")
	}
	if req.MonthlyIncome != nil && *req.MonthlyIncome > maxAmountValue {
		return errBadRequest(c, "monthly_income exceeds maximum allowed value")
	}
	if req.Mode != nil && !validBudgetModes[*req.Mode] {
		return errBadRequest(c, "invalid mode, must be 'manual' or 'guided'")
	}
	if req.Currency != nil && len(*req.Currency) != maxCurrencyLength {
		return errBadRequest(c, "invalid currency code")
	}
	if req.BillingPeriodMonths != nil && *req.BillingPeriodMonths <= 0 {
		return errBadRequest(c, "billing_period_months must be positive")
	}

	// Fetch existing budget to verify ownership.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budgets", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch budget")
	}

	var budgets []models.Budget
	if err := json.Unmarshal(body, &budgets); err != nil || len(budgets) == 0 {
		return errNotFound(c, "budget not found")
	}

	b := budgets[0]

	// Apply partial updates.
	if req.Name != nil {
		b.Name = *req.Name
	}
	if req.MonthlyIncome != nil {
		b.MonthlyIncome = *req.MonthlyIncome
	}
	if req.Currency != nil {
		b.Currency = *req.Currency
	}
	if req.BillingPeriodMonths != nil {
		b.BillingPeriodMonths = *req.BillingPeriodMonths
	}
	if req.Mode != nil {
		b.Mode = *req.Mode
	}
	b.UpdatedAt = time.Now().UTC()

	updatePayload := map[string]interface{}{
		"name":                  b.Name,
		"monthly_income":        b.MonthlyIncome,
		"currency":              b.Currency,
		"billing_period_months": b.BillingPeriodMonths,
		"mode":                  b.Mode,
		"updated_at":            b.UpdatedAt.Format(time.RFC3339Nano),
	}
	updateBytes, err := marshalJSON(updatePayload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	patchQuery := database.NewFilter().
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budgets", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to update budget")
	}

	broadcast(budgetID.String(), ws.MessageTypeBudgetUpdated, b)

	return c.JSON(b)
}

// DeleteBudget deletes a budget and all associated data (expenses, sections,
// categories, collaborators, invites). Only the owner can delete.
// On success it broadcasts a budget_deleted event via WebSocket.
func DeleteBudget(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	// Verify ownership.
	getQuery := database.NewFilter().
		Select("id").
		Eq("id", budgetID.String()).
		Eq("user_id", userID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budgets", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to verify budget ownership")
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return errNotFound(c, "budget not found")
	}

	bid := budgetID.String()

	// 1. Delete expenses.
	expQuery := database.NewFilter().Eq("budget_id", bid).Build()
	_, statusCode, err = database.DB.Delete("budget_expenses", expQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete budget expenses")
	}

	// 2. Get section IDs to delete categories.
	sectionQuery := database.NewFilter().Select("id").Eq("budget_id", bid).Build()
	sectionBody, sectionStatusCode, sectionErr := database.DB.Get("budget_categories", sectionQuery)

	if sectionErr == nil && sectionStatusCode == http.StatusOK {
		var sections []struct{ ID string `json:"id"` }
		if err := json.Unmarshal(sectionBody, &sections); err == nil && len(sections) > 0 {
			sectionIDs := make([]string, len(sections))
			for i, s := range sections {
				sectionIDs[i] = s.ID
			}
			catQuery := database.NewFilter().In("category_id", sectionIDs).Build()
			_, statusCode, err = database.DB.Delete("budget_subcategories", catQuery)
			if err != nil || statusCode >= 300 {
				return errInternal(c, "failed to delete budget categories")
			}
		}
	}

	// 3. Delete sections.
	delSectionQuery := database.NewFilter().Eq("budget_id", bid).Build()
	_, statusCode, err = database.DB.Delete("budget_categories", delSectionQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete budget sections")
	}

	// 4. Delete collaborators.
	collabQuery := database.NewFilter().Eq("budget_id", bid).Build()
	_, statusCode, err = database.DB.Delete("budget_collaborators", collabQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete budget collaborators")
	}

	// 5. Delete invites.
	inviteQuery := database.NewFilter().Eq("budget_id", bid).Build()
	_, statusCode, err = database.DB.Delete("budget_invites", inviteQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete budget invites")
	}

	// 6. Delete the budget.
	delBudgetQuery := database.NewFilter().
		Eq("id", bid).
		Eq("user_id", userID.String()).
		Build()
	_, statusCode, err = database.DB.Delete("budgets", delBudgetQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete budget")
	}

	broadcast(bid, ws.MessageTypeBudgetDeleted, map[string]string{"id": bid})

	return c.SendStatus(fiber.StatusNoContent)
}
