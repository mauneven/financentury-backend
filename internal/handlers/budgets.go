package handlers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// guidedCategory defines a single flat category entry in a budget template.
// Percent represents the percentage of the TOTAL budget income. The former
// two-level section templates are flattened at definition time by multiplying
// the section percentage by the subcategory percentage.
type guidedCategory struct {
	Name      string
	Percent   float64
	Icon      string
	SortOrder int
}

// getBalancedCategories returns the flattened 50/30/10/10 balanced template.
// Each percent is of the monthly income: e.g. "Vivienda" was 45% of the 50%
// Necesidades section, which is 22.5% of the total budget.
func getBalancedCategories() []guidedCategory {
	return []guidedCategory{
		{Name: "Vivienda", Percent: 22.5, Icon: "home", SortOrder: 1},
		{Name: "Comida", Percent: 12.5, Icon: "utensils", SortOrder: 2},
		{Name: "Transporte", Percent: 9, Icon: "car", SortOrder: 3},
		{Name: "Servicios", Percent: 6, Icon: "lightbulb", SortOrder: 4},
		{Name: "Salidas", Percent: 15, Icon: "party", SortOrder: 5},
		{Name: "Entretenimiento", Percent: 15, Icon: "clapperboard", SortOrder: 6},
		{Name: "Tarjetas", Percent: 5, Icon: "credit-card", SortOrder: 7},
		{Name: "Préstamos", Percent: 5, Icon: "landmark", SortOrder: 8},
		{Name: "Fondo de emergencia", Percent: 5, Icon: "landmark", SortOrder: 9},
		{Name: "Inversión", Percent: 5, Icon: "trending", SortOrder: 10},
	}
}

// getDebtFreeCategories returns the flattened 50/30/20 financially-free template.
func getDebtFreeCategories() []guidedCategory {
	return []guidedCategory{
		{Name: "Vivienda", Percent: 22.5, Icon: "home", SortOrder: 1},
		{Name: "Comida", Percent: 12.5, Icon: "utensils", SortOrder: 2},
		{Name: "Transporte", Percent: 9, Icon: "car", SortOrder: 3},
		{Name: "Servicios", Percent: 6, Icon: "lightbulb", SortOrder: 4},
		{Name: "Salidas", Percent: 15, Icon: "party", SortOrder: 5},
		{Name: "Entretenimiento", Percent: 15, Icon: "clapperboard", SortOrder: 6},
		{Name: "Fondo de emergencia", Percent: 10, Icon: "landmark", SortOrder: 7},
		{Name: "Inversión", Percent: 10, Icon: "trending", SortOrder: 8},
	}
}

// getDebtPayoffCategories returns the flattened 50/20/30 debt-payoff template.
func getDebtPayoffCategories() []guidedCategory {
	return []guidedCategory{
		{Name: "Vivienda", Percent: 22.5, Icon: "home", SortOrder: 1},
		{Name: "Comida", Percent: 12.5, Icon: "utensils", SortOrder: 2},
		{Name: "Transporte", Percent: 9, Icon: "car", SortOrder: 3},
		{Name: "Servicios", Percent: 6, Icon: "lightbulb", SortOrder: 4},
		{Name: "Salidas", Percent: 10, Icon: "party", SortOrder: 5},
		{Name: "Entretenimiento", Percent: 10, Icon: "clapperboard", SortOrder: 6},
		{Name: "Tarjetas", Percent: 15, Icon: "credit-card", SortOrder: 7},
		{Name: "Préstamos", Percent: 15, Icon: "landmark", SortOrder: 8},
	}
}

// getTravelCategories returns the flattened 30/30/40 travel-budget template.
func getTravelCategories() []guidedCategory {
	return []guidedCategory{
		{Name: "Vuelos", Percent: 30, Icon: "plane", SortOrder: 1},
		{Name: "Hospedaje", Percent: 30, Icon: "bed", SortOrder: 2},
		{Name: "Comida", Percent: 16, Icon: "utensils", SortOrder: 3},
		{Name: "Actividades", Percent: 14, Icon: "map-pin", SortOrder: 4},
		{Name: "Transporte local", Percent: 10, Icon: "car", SortOrder: 5},
	}
}

// getEventCategories returns the flattened 50/30/20 event-budget template.
func getEventCategories() []guidedCategory {
	return []guidedCategory{
		{Name: "Comida", Percent: 50, Icon: "utensils", SortOrder: 1},
		{Name: "Bebidas", Percent: 30, Icon: "wine", SortOrder: 2},
		{Name: "Decoración", Percent: 8, Icon: "sparkles", SortOrder: 3},
		{Name: "Logística", Percent: 12, Icon: "truck", SortOrder: 4},
	}
}

// getCategoriesForMode returns the flat guided template for the given mode.
func getCategoriesForMode(mode string) []guidedCategory {
	switch mode {
	case "balanced":
		return getBalancedCategories()
	case "debt-free":
		return getDebtFreeCategories()
	case "debt-payoff":
		return getDebtPayoffCategories()
	case "travel":
		return getTravelCategories()
	case "event":
		return getEventCategories()
	default:
		return getBalancedCategories()
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

	// Single query returning both owned and collaborated budgets. This replaces
	// the old 2-3 query pattern (owned list + collab IDs + collab budgets) that
	// required two round-trips plus a second Unmarshal of budget rows.
	reqCtx := c.Context()
	// FIX: monthly_income is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	rows, err := database.DB.Pool.Query(reqCtx, `
		SELECT id, user_id, name, icon, monthly_income::float8, currency,
		       billing_period_months, billing_cutoff_day, mode, created_at, updated_at
		FROM budgets
		WHERE user_id = $1
		   OR id IN (SELECT budget_id FROM budget_collaborators WHERE user_id = $1)
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return errInternal(c, "failed to fetch budgets")
	}
	defer rows.Close()

	budgets := make([]models.Budget, 0)
	for rows.Next() {
		var b models.Budget
		if err := rows.Scan(&b.ID, &b.UserID, &b.Name, &b.Icon, &b.MonthlyIncome,
			&b.Currency, &b.BillingPeriodMonths, &b.BillingCutoffDay, &b.Mode,
			&b.CreatedAt, &b.UpdatedAt); err != nil {
			return errInternal(c, "failed to parse budget row")
		}
		budgets = append(budgets, b)
	}
	if err := rows.Err(); err != nil {
		return errInternal(c, "failed to iterate budgets")
	}

	return c.JSON(budgets)
}

// maxBudgetsPerUser is the maximum number of budgets a user can access
// (owned + collaborated combined).
const maxBudgetsPerUser = 7

// enforceUserBudgetLimit checks that the user hasn't reached the budget cap.
// Returns an error with message "limit" when the cap is hit, or a generic
// error on internal failures. Returns nil when under the limit.
//
// Uses a single UNION ALL query so both counts are computed server-side in one
// round-trip instead of the previous 2-goroutine fan-out (which incurred two
// network round-trips and two connection acquisitions per call).
func enforceUserBudgetLimit(userID uuid.UUID) error {
	var total int
	err := database.DB.Pool.QueryRow(context.Background(),
		`SELECT (
			(SELECT COUNT(*) FROM budgets WHERE user_id = $1)
			+ (SELECT COUNT(*) FROM budget_collaborators WHERE user_id = $1)
		) AS total`, userID).Scan(&total)
	if err != nil {
		return err
	}
	if total >= maxBudgetsPerUser {
		return fmt.Errorf("limit")
	}
	return nil
}

// CreateBudget creates a new budget and optionally seeds guided categories.
// On success it broadcasts a budget_created event via WebSocket.
func CreateBudget(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	// Enforce per-user budget limit (owned + collaborated budgets both count).
	if err := enforceUserBudgetLimit(userID); err != nil {
		if err.Error() == "limit" {
			return errBadRequest(c, "budget limit reached (max 7)")
		}
		return errInternal(c, "failed to check budget count")
	}

	var req models.CreateBudgetRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Sanitize text inputs.
	req.Name = strings.TrimSpace(req.Name)
	req.Icon = strings.TrimSpace(req.Icon)
	req.Currency = strings.TrimSpace(req.Currency)
	req.Mode = strings.TrimSpace(req.Mode)

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
	if req.Mode == "" {
		req.Mode = "manual"
	}
	if req.Icon == "" {
		req.Icon = "wallet"
	}

	// Validate billing_period_months against allowed values.
	if !validBillingPeriodMonths[req.BillingPeriodMonths] {
		return errBadRequest(c, "billing_period_months must be one of: 0, 1, 3, 6, 12")
	}
	// billing_period_months == 0 means "one-time" budget (no billing cycle).
	if req.BillingPeriodMonths > 0 && req.BillingCutoffDay <= 0 {
		req.BillingCutoffDay = 1
	}

	// Validate mode and currency.
	if !validBudgetModes[req.Mode] {
		return errBadRequest(c, "invalid mode")
	}
	if !isValidCurrencyCode(req.Currency) {
		return errBadRequest(c, "invalid currency code (must be 3 uppercase letters)")
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
		Icon:                req.Icon,
		MonthlyIncome:       req.MonthlyIncome,
		Currency:            req.Currency,
		BillingPeriodMonths: req.BillingPeriodMonths,
		BillingCutoffDay:    req.BillingCutoffDay,
		Mode:                req.Mode,
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	// PERF: direct pgx INSERT avoids the extra JSON marshal + DB.Post round-trip
	// through the generic HTTP-style wrapper. One query, only the columns we need.
	if _, err := database.DB.Pool.Exec(c.Context(), `
		INSERT INTO budgets
			(id, user_id, name, icon, monthly_income, currency,
			 billing_period_months, billing_cutoff_day, mode, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)
	`, budget.ID, budget.UserID, budget.Name, budget.Icon, budget.MonthlyIncome,
		budget.Currency, budget.BillingPeriodMonths, budget.BillingCutoffDay,
		budget.Mode, now); err != nil {
		return errInternal(c, "failed to create budget")
	}

	// Seed guided categories for template-based modes.
	if guidedModes[budget.Mode] {
		if err := seedGuidedCategories(budget.ID, budget.Mode, budget.MonthlyIncome, now); err != nil {
			return errInternal(c, "failed to create guided categories")
		}
	}

	// PERF: invalidate any cached summary for the new budget so the first
	// dashboard load after creation doesn't serve a pre-seed shell.
	invalidateBudget(budgetID)

	broadcast(budgetID.String(), ws.MessageTypeBudgetCreated, budget)

	return c.Status(fiber.StatusCreated).JSON(budget)
}

// seedGuidedCategories creates the flat template categories for a guided
// budget mode inside a single transaction. The 50-category cap is enforced at
// the DB level; none of our built-in templates approach it, but the trigger
// remains the last line of defence.
//
// Allocations are rounded to 2 decimals (matching the NUMERIC(18,2) column
// precision) using floor semantics for all but the last entry. The last
// category absorbs the remainder so the sum lands exactly at monthlyIncome
// without ever exceeding it — otherwise rounding could push total_allocation
// above monthly_income and violate the invariant that Create/UpdateCategory
// enforce.
func seedGuidedCategories(budgetID uuid.UUID, mode string, monthlyIncome float64, now time.Time) error {
	categories := getCategoriesForMode(mode)
	if len(categories) == 0 {
		return nil
	}

	// PERF: build the full column arrays in memory, then fire a single
	// INSERT ... SELECT FROM UNNEST(...) statement. Previously we opened a
	// transaction and issued one INSERT per template row (5-10 round-trips
	// per budget create); now it's a single query — no transaction needed
	// because we insert all rows atomically in one statement.
	ids := make([]uuid.UUID, len(categories))
	names := make([]string, len(categories))
	values := make([]float64, len(categories))
	icons := make([]string, len(categories))
	sortOrders := make([]int32, len(categories))

	var allocated float64
	lastIdx := len(categories) - 1
	for i, gc := range categories {
		var catValue float64
		if i == lastIdx {
			// Last entry consumes whatever is left of the income so the sum
			// lands exactly at monthlyIncome (and never above). Guard against
			// FP drift pushing it very slightly negative.
			catValue = math.Round((monthlyIncome-allocated)*100) / 100
			if catValue < 0 {
				catValue = 0
			}
		} else {
			// Floor to 2 decimals so the running total never drifts above
			// the pro-rata share.
			catValue = math.Floor(gc.Percent/100*monthlyIncome*100) / 100
		}
		allocated += catValue

		ids[i] = uuid.New()
		names[i] = gc.Name
		values[i] = catValue
		icons[i] = gc.Icon
		sortOrders[i] = int32(gc.SortOrder)
	}

	_, err := database.DB.Pool.Exec(context.Background(), `
		INSERT INTO budget_categories
			(id, budget_id, name, allocation_value, icon, sort_order, created_at)
		SELECT id, $2, name, allocation_value, icon, sort_order, $7
		FROM UNNEST($1::uuid[], $3::text[], $4::numeric[], $5::text[], $6::int[])
		AS t(id, name, allocation_value, icon, sort_order)
	`, ids, budgetID, names, values, icons, sortOrders, now)
	return err
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

	// PERF: fused access-check + fetch. Previously this ran verifyBudgetAccess
	// (1 query) followed by DB.Get (another query + JSON agg round-trip). Now
	// one SELECT gates on owner OR collaborator via EXISTS and returns only
	// the columns the response serializes — no more SELECT *. If the row is
	// missing, pgx.ErrNoRows maps to 404.
	var b models.Budget
	// FIX: monthly_income is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	err := database.DB.Pool.QueryRow(c.Context(), `
		SELECT id, user_id, name, icon, monthly_income::float8, currency,
		       billing_period_months, billing_cutoff_day, mode, created_at, updated_at
		FROM budgets
		WHERE id = $1
		  AND (user_id = $2
		       OR EXISTS (SELECT 1 FROM budget_collaborators
		                  WHERE budget_id = $1 AND user_id = $2))
	`, budgetID, userID).Scan(
		&b.ID, &b.UserID, &b.Name, &b.Icon, &b.MonthlyIncome, &b.Currency,
		&b.BillingPeriodMonths, &b.BillingCutoffDay, &b.Mode,
		&b.CreatedAt, &b.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "budget not found")
		}
		return errInternal(c, "failed to fetch budget")
	}

	return c.JSON(b)
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

	// Sanitize text inputs.
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		req.Name = &trimmed
	}
	if req.Icon != nil {
		trimmed := strings.TrimSpace(*req.Icon)
		req.Icon = &trimmed
	}
	if req.Currency != nil {
		trimmed := strings.TrimSpace(*req.Currency)
		req.Currency = &trimmed
	}
	if req.Mode != nil {
		trimmed := strings.TrimSpace(*req.Mode)
		req.Mode = &trimmed
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
	if req.Currency != nil && !isValidCurrencyCode(*req.Currency) {
		return errBadRequest(c, "invalid currency code (must be 3 uppercase letters)")
	}
	if req.BillingPeriodMonths != nil && !validBillingPeriodMonths[*req.BillingPeriodMonths] {
		return errBadRequest(c, "billing_period_months must be one of: 0, 1, 3, 6, 12")
	}
	if req.BillingCutoffDay != nil && (*req.BillingCutoffDay < 1 || *req.BillingCutoffDay > 31) {
		return errBadRequest(c, "billing_cutoff_day must be between 1 and 31")
	}

	// PERF: persist inside a transaction that locks the budget row. The
	// initial non-transactional "fetch-to-verify" SELECT has been removed —
	// the FOR UPDATE below both verifies ownership and gives us the current
	// row values, saving one round-trip on every PUT. The lock remains
	// necessary when the caller is lowering monthly_income: without it a
	// concurrent CreateCategory / UpdateCategory (which locks `budgets FOR
	// UPDATE` before checking the allocation ceiling) could slip between our
	// SUM check and our UPDATE, leaving total_allocation > new monthly_income.
	tx, err := database.DB.Pool.Begin(c.Context())
	if err != nil {
		return errInternal(c, "failed to start transaction")
	}
	defer tx.Rollback(c.Context()) //nolint:errcheck

	var b models.Budget
	// FIX: monthly_income is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	if err := tx.QueryRow(c.Context(),
		`SELECT id, user_id, name, icon, monthly_income::float8, currency,
		        billing_period_months, billing_cutoff_day, mode, created_at, updated_at
		   FROM budgets
		  WHERE id = $1 AND user_id = $2
		  FOR UPDATE`,
		budgetID, userID).Scan(
		&b.ID, &b.UserID, &b.Name, &b.Icon, &b.MonthlyIncome, &b.Currency,
		&b.BillingPeriodMonths, &b.BillingCutoffDay, &b.Mode,
		&b.CreatedAt, &b.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "budget not found")
		}
		return errInternal(c, "failed to fetch budget")
	}

	if req.MonthlyIncome != nil && *req.MonthlyIncome < b.MonthlyIncome {
		// Already hold the row lock from SELECT FOR UPDATE above; only need
		// to check the total allocation against the proposed new income.
		var totalAlloc float64
		// FIX: SUM(allocation_value) is NUMERIC — pgx binary format can't scan NUMERIC→float64
		// without ::float8 cast.
		if err := tx.QueryRow(c.Context(),
			`SELECT COALESCE(SUM(allocation_value), 0)::float8 FROM budget_categories WHERE budget_id = $1`,
			budgetID).Scan(&totalAlloc); err != nil {
			return errInternal(c, "failed to verify existing allocations")
		}
		if totalAlloc > *req.MonthlyIncome {
			return errBadRequest(c, "monthly_income is less than total category allocations; reduce category allocations first")
		}
	}

	// Apply partial updates.
	if req.Name != nil {
		b.Name = *req.Name
	}
	if req.Icon != nil {
		b.Icon = *req.Icon
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

	if _, err := tx.Exec(c.Context(),
		`UPDATE budgets
		 SET name = $1, icon = $2, monthly_income = $3, currency = $4,
		     billing_period_months = $5, billing_cutoff_day = $6, mode = $7, updated_at = $8
		 WHERE id = $9 AND user_id = $10`,
		b.Name, b.Icon, b.MonthlyIncome, b.Currency, b.BillingPeriodMonths,
		b.BillingCutoffDay, b.Mode, b.UpdatedAt, budgetID, userID); err != nil {
		return errInternal(c, "failed to update budget")
	}

	if err := tx.Commit(c.Context()); err != nil {
		return errInternal(c, "failed to commit budget update")
	}

	// PERF: drop cached summary so the next GET reflects the new income /
	// metadata immediately.
	invalidateBudget(budgetID)

	broadcast(budgetID.String(), ws.MessageTypeBudgetUpdated, b)

	return c.JSON(b)
}

// DeleteBudget deletes a budget and all associated data (expenses, categories,
// collaborators, invites, links). Only the owner can delete.
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

	bid := budgetID.String()
	reqCtx := c.Context()

	// PERF: delete budget in a transaction — CASCADE handles expenses,
	// categories, collaborators, and invites. The DELETE...RETURNING against
	// (id,user_id) replaces the previous "SELECT id to verify + DELETE"
	// pair: one round-trip now both verifies ownership and performs the
	// deletion. Missing row -> pgx.ErrNoRows -> 404.
	tx, err := database.DB.Pool.Begin(reqCtx)
	if err != nil {
		return errInternal(c, "failed to start transaction")
	}
	defer tx.Rollback(reqCtx) //nolint:errcheck

	var deletedID uuid.UUID
	if err := tx.QueryRow(reqCtx,
		`DELETE FROM budgets WHERE id = $1 AND user_id = $2 RETURNING id`,
		budgetID, userID).Scan(&deletedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "budget not found")
		}
		return errInternal(c, "failed to delete budget")
	}

	// Also clean up budget_links where this user created them (created_by FK has no CASCADE).
	if _, err := tx.Exec(reqCtx,
		`DELETE FROM budget_links WHERE created_by = $1 AND (source_budget_id = $2 OR target_budget_id = $2)`,
		userID, budgetID); err != nil {
		return errInternal(c, "failed to clean up budget links")
	}

	if err := tx.Commit(reqCtx); err != nil {
		return errInternal(c, "failed to commit deletion")
	}

	// Budget delete cascade-removes its links: purge the cache for this budget
	// (it could be the source of some links) and for any budget that we
	// couldn't identify without another query.
	invalidateLinkTargetsCacheAll()
	invalidateBudget(budgetID)

	broadcast(bid, ws.MessageTypeBudgetDeleted, map[string]string{"id": bid})

	return c.SendStatus(fiber.StatusNoContent)
}
