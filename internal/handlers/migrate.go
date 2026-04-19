package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
)

// Migration limits to prevent abuse.
const (
	maxMigrateBudgets             = 20
	maxMigrateCategoriesPerBudget = 50
	maxMigrateExpensesPerBudget   = 10000
)

// --- Migration Request Types ---

// MigrateRequest is the top-level migration payload.
type MigrateRequest struct {
	Budgets []MigrateBudget `json:"budgets"`
}

// MigrateBudget represents a budget to migrate. In the flat-category model
// sections do not exist; each budget has a flat list of categories.
type MigrateBudget struct {
	Name                string            `json:"name"`
	MonthlyIncome       float64           `json:"monthly_income"`
	Currency            string            `json:"currency"`
	BillingPeriodMonths int               `json:"billing_period_months"`
	Mode                string            `json:"mode"`
	Categories          []MigrateCategory `json:"categories"`
	Expenses            []MigrateExpense  `json:"expenses"`
}

// MigrateCategory represents a flat category to migrate.
type MigrateCategory struct {
	Name            string  `json:"name"`
	AllocationValue float64 `json:"allocation_value"`
	Icon            string  `json:"icon"`
	SortOrder       int     `json:"sort_order"`
	LocalID         string  `json:"local_id"`
}

// MigrateExpense represents an expense to migrate. local_category_id references
// a MigrateCategory.LocalID in the same budget.
type MigrateExpense struct {
	LocalCategoryID string  `json:"local_category_id"`
	Amount          float64 `json:"amount"`
	Description     string  `json:"description"`
	ExpenseDate     string  `json:"expense_date"`
}

// Migrate handles POST /api/migrate (protected). It imports budgets, flat
// categories, and expenses from client-side local data.
func Migrate(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	var req MigrateRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	if len(req.Budgets) == 0 {
		return errBadRequest(c, "at least one budget is required")
	}
	if len(req.Budgets) > maxMigrateBudgets {
		return errBadRequest(c, fmt.Sprintf("too many budgets in migration (max %d)", maxMigrateBudgets))
	}

	// Security: enforce the per-user budget limit BEFORE each insertion.
	// Previously the check ran once up-front, which allowed a user sitting
	// below the cap to push the migration request for up to 20 budgets and
	// blow past the maxBudgetsPerUser ceiling.
	var createdBudgets []models.Budget

	for _, mb := range req.Budgets {
		if err := enforceUserBudgetLimit(userID); err != nil {
			if err.Error() == "limit" {
				return c.Status(fiber.StatusConflict).JSON(models.ErrorResponse{Error: "budget limit reached (max 7)"})
			}
			return errInternal(c, "failed to check budget count")
		}

		budget, err := migrateSingleBudget(userID, mb)
		if err != nil {
			// Return the error as-is (it's already a *fiber.Error or bad-request).
			if fiberErr, ok := err.(*fiber.Error); ok {
				return c.Status(fiberErr.Code).JSON(models.ErrorResponse{Error: fiberErr.Message})
			}
			return errInternal(c, err.Error())
		}
		createdBudgets = append(createdBudgets, budget)
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"budgets": createdBudgets,
	})
}

// migrateSingleBudget validates and creates a single budget with its flat
// categories and expenses.
func migrateSingleBudget(userID uuid.UUID, mb MigrateBudget) (models.Budget, error) {
	now := time.Now().UTC()
	budgetID := uuid.New()

	// Sanitize text inputs.
	mb.Name = strings.TrimSpace(mb.Name)
	mb.Currency = strings.TrimSpace(mb.Currency)
	mb.Mode = strings.TrimSpace(mb.Mode)

	// Validate budget fields.
	if mb.Name == "" {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, "budget name is required")
	}
	if len(mb.Name) > maxNameLength {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, "budget name too long (max 200 characters)")
	}
	if mb.MonthlyIncome <= 0 {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, "monthly_income must be positive")
	}
	if mb.MonthlyIncome > maxAmountValue {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, "monthly_income exceeds maximum allowed value")
	}
	if len(mb.Categories) > maxMigrateCategoriesPerBudget {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("too many categories per budget (max %d)", maxMigrateCategoriesPerBudget))
	}
	if len(mb.Expenses) > maxMigrateExpensesPerBudget {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("too many expenses per budget (max %d)", maxMigrateExpensesPerBudget))
	}

	// Defaults.
	if mb.Currency == "" {
		mb.Currency = "COP"
	}
	if !validBillingPeriodMonths[mb.BillingPeriodMonths] {
		mb.BillingPeriodMonths = 1
	}
	if mb.Mode == "" {
		mb.Mode = "manual"
	}
	if !validBudgetModes[mb.Mode] {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, "invalid mode")
	}
	if !isValidCurrencyCode(mb.Currency) {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, "invalid currency code (must be 3 uppercase letters)")
	}

	// Validate all categories and expenses up-front so we can bail out BEFORE
	// opening the transaction.
	catLocalIDMap := make(map[string]uuid.UUID)
	type preparedCategory struct {
		id  uuid.UUID
		raw MigrateCategory
	}
	prepared := make([]preparedCategory, 0, len(mb.Categories))
	for _, mc := range mb.Categories {
		if err := validateMigrateCategory(mc); err != nil {
			return models.Budget{}, err
		}
		cat := preparedCategory{id: uuid.New(), raw: mc}
		prepared = append(prepared, cat)
		if mc.LocalID != "" {
			catLocalIDMap[mc.LocalID] = cat.id
		}
	}
	for _, me := range mb.Expenses {
		if err := validateMigrateExpense(me); err != nil {
			return models.Budget{}, err
		}
	}

	// Single transaction with UNNEST-based batch inserts. Every table gets
	// exactly one INSERT.
	ctx := context.Background()
	tx, err := database.DB.Pool.Begin(ctx)
	if err != nil {
		return models.Budget{}, fiber.NewError(fiber.StatusInternalServerError, "failed to start migration transaction")
	}
	defer tx.Rollback(ctx)

	// 1. Budget row.
	_, err = tx.Exec(ctx, `
		INSERT INTO budgets (id, user_id, name, monthly_income, currency,
		                     billing_period_months, mode, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
	`, budgetID, userID, mb.Name, mb.MonthlyIncome, mb.Currency,
		mb.BillingPeriodMonths, mb.Mode, now)
	if err != nil {
		return models.Budget{}, fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("failed to create budget: %s", mb.Name))
	}

	// 2. Flat categories (one INSERT via UNNEST).
	if len(prepared) > 0 {
		cIDs := make([]uuid.UUID, len(prepared))
		cNames := make([]string, len(prepared))
		cAlloc := make([]float64, len(prepared))
		cIcons := make([]string, len(prepared))
		cOrders := make([]int, len(prepared))
		for i, cat := range prepared {
			cIDs[i] = cat.id
			cNames[i] = cat.raw.Name
			cAlloc[i] = cat.raw.AllocationValue
			cIcons[i] = cat.raw.Icon
			cOrders[i] = cat.raw.SortOrder
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO budget_categories (id, budget_id, name, allocation_value, icon, sort_order, created_at)
			SELECT id, $1, name, allocation_value, icon, sort_order, $2
			FROM unnest($3::uuid[], $4::text[], $5::numeric[], $6::text[], $7::int[])
			   AS u(id, name, allocation_value, icon, sort_order)
		`, budgetID, now, cIDs, cNames, cAlloc, cIcons, cOrders)
		if err != nil {
			return models.Budget{}, fiber.NewError(fiber.StatusInternalServerError, "failed to create categories")
		}
	}

	// 3. Expenses (one INSERT via UNNEST; rows with unknown category are
	// silently skipped, matching the previous behaviour).
	type expenseRow struct {
		id        uuid.UUID
		catID     uuid.UUID
		amount    float64
		descr     string
		expenseDt string
	}
	expRows := make([]expenseRow, 0, len(mb.Expenses))
	for _, me := range mb.Expenses {
		realCatID, ok := catLocalIDMap[me.LocalCategoryID]
		if !ok {
			continue
		}
		expenseDate := me.ExpenseDate
		if expenseDate == "" {
			expenseDate = now.Format(dateFormat)
		}
		expRows = append(expRows, expenseRow{
			id:        uuid.New(),
			catID:     realCatID,
			amount:    me.Amount,
			descr:     me.Description,
			expenseDt: expenseDate,
		})
	}
	if len(expRows) > 0 {
		eIDs := make([]uuid.UUID, len(expRows))
		eCats := make([]uuid.UUID, len(expRows))
		eAmts := make([]float64, len(expRows))
		eDescr := make([]string, len(expRows))
		eDates := make([]string, len(expRows))
		for i, r := range expRows {
			eIDs[i] = r.id
			eCats[i] = r.catID
			eAmts[i] = r.amount
			eDescr[i] = r.descr
			eDates[i] = r.expenseDt
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO budget_expenses (id, budget_id, category_id, amount, description, expense_date, created_at)
			SELECT id, $1, category_id, amount, description, expense_date::date, $2
			FROM unnest($3::uuid[], $4::uuid[], $5::numeric[], $6::text[], $7::text[])
			   AS u(id, category_id, amount, description, expense_date)
		`, budgetID, now, eIDs, eCats, eAmts, eDescr, eDates)
		if err != nil {
			return models.Budget{}, fiber.NewError(fiber.StatusInternalServerError, "failed to create expenses")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return models.Budget{}, fiber.NewError(fiber.StatusInternalServerError, "failed to commit migration")
	}

	return models.Budget{
		ID:                  budgetID,
		UserID:              userID,
		Name:                mb.Name,
		MonthlyIncome:       mb.MonthlyIncome,
		Currency:            mb.Currency,
		BillingPeriodMonths: mb.BillingPeriodMonths,
		Mode:                mb.Mode,
		CreatedAt:           now,
		UpdatedAt:           now,
	}, nil
}

// validateMigrateCategory checks migration category fields.
func validateMigrateCategory(mc MigrateCategory) error {
	mc.Name = strings.TrimSpace(mc.Name)
	mc.Icon = strings.TrimSpace(mc.Icon)
	if mc.Name == "" || len(mc.Name) > maxNameLength {
		return fiber.NewError(fiber.StatusBadRequest, "category name is required and must not exceed 200 characters")
	}
	if len(mc.Icon) > maxIconLength {
		return fiber.NewError(fiber.StatusBadRequest, "category icon too long (max 50 characters)")
	}
	if mc.AllocationValue < 0 {
		return fiber.NewError(fiber.StatusBadRequest, "category allocation_value must be positive")
	}
	return nil
}

// validateMigrateExpense checks migration expense fields.
func validateMigrateExpense(me MigrateExpense) error {
	me.Description = strings.TrimSpace(me.Description)
	me.ExpenseDate = strings.TrimSpace(me.ExpenseDate)
	if me.Amount <= 0 {
		return fiber.NewError(fiber.StatusBadRequest, "expense amount must be positive")
	}
	if me.Amount > maxAmountValue {
		return fiber.NewError(fiber.StatusBadRequest, "expense amount exceeds maximum allowed value")
	}
	if len(me.Description) > maxDescriptionLength {
		return fiber.NewError(fiber.StatusBadRequest, "expense description too long (max 500 characters)")
	}
	if me.ExpenseDate != "" && !isValidDate(me.ExpenseDate) {
		return fiber.NewError(fiber.StatusBadRequest, "invalid expense date format, use YYYY-MM-DD")
	}
	if me.ExpenseDate != "" && isDateTooFarInFuture(me.ExpenseDate) {
		return fiber.NewError(fiber.StatusBadRequest, "expense date cannot be more than 1 year in the future")
	}
	return nil
}
