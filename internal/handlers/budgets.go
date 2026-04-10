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
	"golang.org/x/sync/errgroup"
)

// guidedSection defines a section for a budget template mode.
type guidedSection struct {
	Name       string
	Percent    float64
	Icon       string
	SortOrder  int
	Categories []guidedCategory
}

// guidedCategory defines a category within a guided section.
// Percent represents the percentage of the PARENT SECTION (not the total budget).
type guidedCategory struct {
	Name      string
	Percent   float64
	Icon      string
	SortOrder int
}

// getBalancedSections returns the 50/30/10/10 balanced template.
func getBalancedSections() []guidedSection {
	return []guidedSection{
		{
			Name: "Necesidades", Percent: 50, Icon: "home", SortOrder: 1,
			Categories: []guidedCategory{
				{Name: "Vivienda", Percent: 45, Icon: "home", SortOrder: 1},
				{Name: "Comida", Percent: 25, Icon: "utensils", SortOrder: 2},
				{Name: "Transporte", Percent: 18, Icon: "car", SortOrder: 3},
				{Name: "Servicios", Percent: 12, Icon: "lightbulb", SortOrder: 4},
			},
		},
		{
			Name: "Deseos", Percent: 30, Icon: "party", SortOrder: 2,
			Categories: []guidedCategory{
				{Name: "Salidas", Percent: 50, Icon: "party", SortOrder: 1},
				{Name: "Entretenimiento", Percent: 50, Icon: "clapperboard", SortOrder: 2},
			},
		},
		{
			Name: "Deudas", Percent: 10, Icon: "credit-card", SortOrder: 3,
			Categories: []guidedCategory{
				{Name: "Tarjetas", Percent: 50, Icon: "credit-card", SortOrder: 1},
				{Name: "Préstamos", Percent: 50, Icon: "landmark", SortOrder: 2},
			},
		},
		{
			Name: "Ahorro", Percent: 10, Icon: "coins", SortOrder: 4,
			Categories: []guidedCategory{
				{Name: "Fondo de emergencia", Percent: 50, Icon: "landmark", SortOrder: 1},
				{Name: "Inversión", Percent: 50, Icon: "trending", SortOrder: 2},
			},
		},
	}
}

// getDebtFreeSections returns the 50/30/20 financially free template.
func getDebtFreeSections() []guidedSection {
	return []guidedSection{
		{
			Name: "Necesidades", Percent: 50, Icon: "home", SortOrder: 1,
			Categories: []guidedCategory{
				{Name: "Vivienda", Percent: 45, Icon: "home", SortOrder: 1},
				{Name: "Comida", Percent: 25, Icon: "utensils", SortOrder: 2},
				{Name: "Transporte", Percent: 18, Icon: "car", SortOrder: 3},
				{Name: "Servicios", Percent: 12, Icon: "lightbulb", SortOrder: 4},
			},
		},
		{
			Name: "Deseos", Percent: 30, Icon: "party", SortOrder: 2,
			Categories: []guidedCategory{
				{Name: "Salidas", Percent: 50, Icon: "party", SortOrder: 1},
				{Name: "Entretenimiento", Percent: 50, Icon: "clapperboard", SortOrder: 2},
			},
		},
		{
			Name: "Ahorro", Percent: 20, Icon: "coins", SortOrder: 3,
			Categories: []guidedCategory{
				{Name: "Fondo de emergencia", Percent: 50, Icon: "landmark", SortOrder: 1},
				{Name: "Inversión", Percent: 50, Icon: "trending", SortOrder: 2},
			},
		},
	}
}

// getDebtPayoffSections returns the 50/20/30 debt payoff template.
func getDebtPayoffSections() []guidedSection {
	return []guidedSection{
		{
			Name: "Necesidades", Percent: 50, Icon: "home", SortOrder: 1,
			Categories: []guidedCategory{
				{Name: "Vivienda", Percent: 45, Icon: "home", SortOrder: 1},
				{Name: "Comida", Percent: 25, Icon: "utensils", SortOrder: 2},
				{Name: "Transporte", Percent: 18, Icon: "car", SortOrder: 3},
				{Name: "Servicios", Percent: 12, Icon: "lightbulb", SortOrder: 4},
			},
		},
		{
			Name: "Deseos", Percent: 20, Icon: "party", SortOrder: 2,
			Categories: []guidedCategory{
				{Name: "Salidas", Percent: 50, Icon: "party", SortOrder: 1},
				{Name: "Entretenimiento", Percent: 50, Icon: "clapperboard", SortOrder: 2},
			},
		},
		{
			Name: "Deuda", Percent: 30, Icon: "credit-card", SortOrder: 3,
			Categories: []guidedCategory{
				{Name: "Tarjetas", Percent: 50, Icon: "credit-card", SortOrder: 1},
				{Name: "Préstamos", Percent: 50, Icon: "landmark", SortOrder: 2},
			},
		},
	}
}

// getTravelSections returns the 30/30/40 travel budget template.
func getTravelSections() []guidedSection {
	return []guidedSection{
		{
			Name: "Vuelos", Percent: 30, Icon: "plane", SortOrder: 1,
			Categories: []guidedCategory{
				{Name: "Vuelos", Percent: 100, Icon: "plane", SortOrder: 1},
			},
		},
		{
			Name: "Hospedaje", Percent: 30, Icon: "bed", SortOrder: 2,
			Categories: []guidedCategory{
				{Name: "Hospedaje", Percent: 100, Icon: "bed", SortOrder: 1},
			},
		},
		{
			Name: "Salidas", Percent: 40, Icon: "party", SortOrder: 3,
			Categories: []guidedCategory{
				{Name: "Comida", Percent: 40, Icon: "utensils", SortOrder: 1},
				{Name: "Actividades", Percent: 35, Icon: "map-pin", SortOrder: 2},
				{Name: "Transporte local", Percent: 25, Icon: "car", SortOrder: 3},
			},
		},
	}
}

// getEventSections returns the 50/30/20 event budget template.
func getEventSections() []guidedSection {
	return []guidedSection{
		{
			Name: "Comida", Percent: 50, Icon: "utensils", SortOrder: 1,
			Categories: []guidedCategory{
				{Name: "Comida", Percent: 100, Icon: "utensils", SortOrder: 1},
			},
		},
		{
			Name: "Bebidas", Percent: 30, Icon: "wine", SortOrder: 2,
			Categories: []guidedCategory{
				{Name: "Bebidas", Percent: 100, Icon: "wine", SortOrder: 1},
			},
		},
		{
			Name: "Gestión", Percent: 20, Icon: "settings", SortOrder: 3,
			Categories: []guidedCategory{
				{Name: "Decoración", Percent: 40, Icon: "sparkles", SortOrder: 1},
				{Name: "Logística", Percent: 60, Icon: "truck", SortOrder: 2},
			},
		},
	}
}

// getSectionsForMode returns the guided template for the given mode.
func getSectionsForMode(mode string) []guidedSection {
	switch mode {
	case "balanced":
		return getBalancedSections()
	case "debt-free":
		return getDebtFreeSections()
	case "debt-payoff":
		return getDebtPayoffSections()
	case "travel":
		return getTravelSections()
	case "event":
		return getEventSections()
	default:
		return getBalancedSections()
	}
}

// ListBudgets returns all budgets for the authenticated user (owned + collaborative).
// Supports limit/offset pagination via query params.
func ListBudgets(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	limit, offset := parsePaginationParams(c)
	uid := userID.String()

	var (
		budgets []models.Budget
		collabs []struct {
			BudgetID string `json:"budget_id"`
		}
	)

	// Run the owned-budgets query and collaborator-IDs query in parallel.
	g := new(errgroup.Group)

	g.Go(func() error {
		query := database.NewFilter().
			Select("*").
			Eq("user_id", uid).
			Order("created_at", "desc").
			Limit(limit).
			Offset(offset).
			Build()

		body, statusCode, err := database.DB.Get("budgets", query)
		if err != nil || statusCode != http.StatusOK {
			return fiber.ErrInternalServerError
		}

		if err := json.Unmarshal(body, &budgets); err != nil {
			return fiber.ErrInternalServerError
		}
		return nil
	})

	g.Go(func() error {
		collabQuery := database.NewFilter().
			Select("budget_id").
			Eq("user_id", uid).
			Build()

		collabBody, collabStatus, collabErr := database.DB.Get("budget_collaborators", collabQuery)
		if collabErr != nil || collabStatus != http.StatusOK {
			// Non-fatal: user may simply have no collaborations.
			return nil
		}

		_ = json.Unmarshal(collabBody, &collabs)
		return nil
	})

	if err := g.Wait(); err != nil {
		return errInternal(c, "failed to fetch budgets")
	}

	if budgets == nil {
		budgets = make([]models.Budget, 0)
	}

	// Fetch the actual collaborative budgets (sequential; depends on collabs result).
	if len(collabs) > 0 {
		budgetIDs := make([]string, len(collabs))
		for i, cb := range collabs {
			budgetIDs[i] = cb.BudgetID
		}

		collabBudgetQuery := database.NewFilter().
			Select("*").
			In("id", budgetIDs).
			Order("created_at", "desc").
			Limit(limit).
			Offset(offset).
			Build()

		collabBudgetBody, collabBudgetStatus, collabBudgetErr := database.DB.Get("budgets", collabBudgetQuery)
		if collabBudgetErr == nil && collabBudgetStatus == http.StatusOK {
			var collabBudgets []models.Budget
			if err := json.Unmarshal(collabBudgetBody, &collabBudgets); err == nil {
				budgets = append(budgets, collabBudgets...)
			}
		}
	}

	return c.JSON(budgets)
}

// maxBudgetsPerUser is the maximum number of budgets a single user can own.
const maxBudgetsPerUser = 7

// CreateBudget creates a new budget and optionally seeds guided sections.
// On success it broadcasts a budget_created event via WebSocket.
func CreateBudget(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	// Enforce per-user budget limit.
	countQuery := database.NewFilter().
		Select("id").
		Eq("user_id", userID.String()).
		Build()

	countBody, countStatus, countErr := database.DB.Get("budgets", countQuery)
	if countErr != nil || countStatus != http.StatusOK {
		return errInternal(c, "failed to check budget count")
	}

	var existing []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(countBody, &existing); err != nil {
		return errInternal(c, "failed to parse budget count")
	}
	if len(existing) >= maxBudgetsPerUser {
		return errBadRequest(c, "budget limit reached (max 7)")
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
	if req.BillingPeriodMonths < 0 {
		req.BillingPeriodMonths = 1
	}
	// billing_period_months == 0 means "one-time" budget (no billing cycle).
	if req.BillingPeriodMonths > 0 && req.BillingCutoffDay <= 0 {
		req.BillingCutoffDay = 1
	}
	if req.Mode == "" {
		req.Mode = "manual"
	}

	// Validate mode and currency.
	if !validBudgetModes[req.Mode] {
		return errBadRequest(c, "invalid mode")
	}
	if len(req.Currency) != maxCurrencyLength {
		return errBadRequest(c, "invalid currency code")
	}
	if req.BillingPeriodMonths > 0 && (req.BillingCutoffDay < 1 || req.BillingCutoffDay > 31) {
		return errBadRequest(c, "billing_cutoff_day must be between 1 and 31")
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
		BillingCutoffDay:    req.BillingCutoffDay,
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
		"billing_cutoff_day":    budget.BillingCutoffDay,
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

	// Seed guided sections for template-based modes.
	if guidedModes[budget.Mode] {
		if err := seedGuidedSections(budget.ID, budget.Mode, now); err != nil {
			return errInternal(c, "failed to create guided sections")
		}
	}

	broadcast(budgetID.String(), ws.MessageTypeBudgetCreated, budget)

	return c.Status(fiber.StatusCreated).JSON(budget)
}

// seedGuidedSections creates template sections and categories based on the budget mode.
func seedGuidedSections(budgetID uuid.UUID, mode string, now time.Time) error {
	for _, gs := range getSectionsForMode(mode) {
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
		return errBadRequest(c, "invalid mode")
	}
	if req.Currency != nil && len(*req.Currency) != maxCurrencyLength {
		return errBadRequest(c, "invalid currency code")
	}
	if req.BillingPeriodMonths != nil && *req.BillingPeriodMonths < 0 {
		return errBadRequest(c, "billing_period_months must be zero or positive")
	}
	if req.BillingCutoffDay != nil && (*req.BillingCutoffDay < 0 || *req.BillingCutoffDay > 31) {
		return errBadRequest(c, "billing_cutoff_day must be between 0 and 31")
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
	if req.BillingCutoffDay != nil {
		b.BillingCutoffDay = *req.BillingCutoffDay
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
		"billing_cutoff_day":    b.BillingCutoffDay,
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
