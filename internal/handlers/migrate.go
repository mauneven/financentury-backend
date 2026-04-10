package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
)

// Migration limits to prevent abuse.
const (
	maxMigrateBudgets            = 20
	maxMigrateSectionsPerBudget  = 100
	maxMigrateCategoriesPerGroup = 100
	maxMigrateExpensesPerBudget  = 10000
)

// --- Migration Request Types ---

// MigrateRequest is the top-level migration payload.
type MigrateRequest struct {
	Budgets []MigrateBudget `json:"budgets"`
}

// MigrateBudget represents a budget to migrate.
type MigrateBudget struct {
	Name                string           `json:"name"`
	MonthlyIncome       float64          `json:"monthly_income"`
	Currency            string           `json:"currency"`
	BillingPeriodMonths int              `json:"billing_period_months"`
	Mode                string           `json:"mode"`
	Sections            []MigrateSection `json:"sections"`
	Expenses            []MigrateExpense `json:"expenses"`
}

// MigrateSection represents a section to migrate.
type MigrateSection struct {
	Name              string            `json:"name"`
	AllocationPercent float64           `json:"allocation_percent"`
	Icon              string            `json:"icon"`
	SortOrder         int               `json:"sort_order"`
	LocalID           string            `json:"local_id"`
	Categories        []MigrateCategory `json:"categories"`
}

// MigrateCategory represents a category to migrate.
type MigrateCategory struct {
	Name              string  `json:"name"`
	AllocationPercent float64 `json:"allocation_percent"`
	Icon              string  `json:"icon"`
	SortOrder         int     `json:"sort_order"`
	LocalID           string  `json:"local_id"`
}

// MigrateExpense represents an expense to migrate.
type MigrateExpense struct {
	LocalCategoryID string  `json:"local_category_id"`
	Amount          float64 `json:"amount"`
	Description     string  `json:"description"`
	ExpenseDate     string  `json:"expense_date"`
}

// Migrate handles POST /api/migrate (protected). It imports budgets,
// sections, categories, and expenses from client-side local data.
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

	var createdBudgets []models.Budget

	for _, mb := range req.Budgets {
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

// migrateSingleBudget validates and creates a single budget with its sections,
// categories, and expenses.
func migrateSingleBudget(userID uuid.UUID, mb MigrateBudget) (models.Budget, error) {
	now := time.Now().UTC()
	budgetID := uuid.New()

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
	if len(mb.Sections) > maxMigrateSectionsPerBudget {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("too many sections per budget (max %d)", maxMigrateSectionsPerBudget))
	}
	if len(mb.Expenses) > maxMigrateExpensesPerBudget {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("too many expenses per budget (max %d)", maxMigrateExpensesPerBudget))
	}

	// Defaults.
	if mb.Currency == "" {
		mb.Currency = "COP"
	}
	if mb.BillingPeriodMonths <= 0 {
		mb.BillingPeriodMonths = 1
	}
	if mb.Mode == "" {
		mb.Mode = "manual"
	}
	if !validBudgetModes[mb.Mode] {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, "invalid mode")
	}
	if len(mb.Currency) != maxCurrencyLength {
		return models.Budget{}, fiber.NewError(fiber.StatusBadRequest, "invalid currency code")
	}

	// Create the budget.
	budgetPayload := map[string]interface{}{
		"id":                    budgetID.String(),
		"user_id":               userID.String(),
		"name":                  mb.Name,
		"monthly_income":        mb.MonthlyIncome,
		"currency":              mb.Currency,
		"billing_period_months": mb.BillingPeriodMonths,
		"mode":                  mb.Mode,
		"created_at":            now.Format(time.RFC3339Nano),
		"updated_at":            now.Format(time.RFC3339Nano),
	}
	budgetBytes, err := marshalJSON(budgetPayload)
	if err != nil {
		return models.Budget{}, fiber.ErrInternalServerError
	}

	_, statusCode, err := database.DB.Post("budgets", budgetBytes)
	if err != nil || statusCode != http.StatusCreated {
		return models.Budget{}, fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("failed to create budget: %s", mb.Name))
	}

	// Build localID -> real UUID map for categories.
	catLocalIDMap := make(map[string]uuid.UUID)

	for _, ms := range mb.Sections {
		if err := validateMigrateSection(ms); err != nil {
			return models.Budget{}, err
		}

		sectionID := uuid.New()
		sectionPayload := map[string]interface{}{
			"id":                 sectionID.String(),
			"budget_id":          budgetID.String(),
			"name":               ms.Name,
			"allocation_percent": ms.AllocationPercent,
			"icon":               ms.Icon,
			"sort_order":         ms.SortOrder,
			"created_at":         now.Format(time.RFC3339Nano),
		}
		sectionBytes, err := marshalJSON(sectionPayload)
		if err != nil {
			return models.Budget{}, fiber.ErrInternalServerError
		}

		_, statusCode, err := database.DB.Post("budget_categories", sectionBytes)
		if err != nil || statusCode != http.StatusCreated {
			return models.Budget{}, fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("failed to create section: %s", ms.Name))
		}

		for _, mc := range ms.Categories {
			if err := validateMigrateCategory(mc); err != nil {
				return models.Budget{}, err
			}

			catID := uuid.New()
			if mc.LocalID != "" {
				catLocalIDMap[mc.LocalID] = catID
			}

			catPayload := map[string]interface{}{
				"id":                 catID.String(),
				"category_id":        sectionID.String(),
				"name":               mc.Name,
				"allocation_percent": mc.AllocationPercent,
				"icon":               mc.Icon,
				"sort_order":         mc.SortOrder,
				"created_at":         now.Format(time.RFC3339Nano),
			}
			catBytes, err := marshalJSON(catPayload)
			if err != nil {
				return models.Budget{}, fiber.ErrInternalServerError
			}

			_, statusCode, err := database.DB.Post("budget_subcategories", catBytes)
			if err != nil || statusCode != http.StatusCreated {
				return models.Budget{}, fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("failed to create category: %s", mc.Name))
			}
		}
	}

	// Create expenses.
	for _, me := range mb.Expenses {
		if err := validateMigrateExpense(me); err != nil {
			return models.Budget{}, err
		}

		realCatID, ok := catLocalIDMap[me.LocalCategoryID]
		if !ok {
			continue // Skip unknown category references.
		}

		expenseDate := me.ExpenseDate
		if expenseDate == "" {
			expenseDate = now.Format(dateFormat)
		}

		expPayload := map[string]interface{}{
			"id":             uuid.New().String(),
			"budget_id":      budgetID.String(),
			"subcategory_id": realCatID.String(),
			"amount":         me.Amount,
			"description":    me.Description,
			"expense_date":   expenseDate,
			"created_at":     now.Format(time.RFC3339Nano),
		}
		expBytes, err := marshalJSON(expPayload)
		if err != nil {
			return models.Budget{}, fiber.ErrInternalServerError
		}

		_, statusCode, err := database.DB.Post("budget_expenses", expBytes)
		if err != nil || statusCode != http.StatusCreated {
			return models.Budget{}, fiber.NewError(fiber.StatusInternalServerError, fmt.Sprintf("failed to create expense: %s", me.Description))
		}
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

// validateMigrateSection checks migration section fields.
func validateMigrateSection(ms MigrateSection) error {
	if ms.Name == "" || len(ms.Name) > maxNameLength {
		return fiber.NewError(fiber.StatusBadRequest, "section name is required and must not exceed 200 characters")
	}
	if len(ms.Icon) > maxIconLength {
		return fiber.NewError(fiber.StatusBadRequest, "section icon too long (max 50 characters)")
	}
	if ms.AllocationPercent < 0 || ms.AllocationPercent > 100 {
		return fiber.NewError(fiber.StatusBadRequest, "section allocation_percent must be between 0 and 100")
	}
	if len(ms.Categories) > maxMigrateCategoriesPerGroup {
		return fiber.NewError(fiber.StatusBadRequest, fmt.Sprintf("too many categories per section (max %d)", maxMigrateCategoriesPerGroup))
	}
	return nil
}

// validateMigrateCategory checks migration category fields.
func validateMigrateCategory(mc MigrateCategory) error {
	if mc.Name == "" || len(mc.Name) > maxNameLength {
		return fiber.NewError(fiber.StatusBadRequest, "category name is required and must not exceed 200 characters")
	}
	if len(mc.Icon) > maxIconLength {
		return fiber.NewError(fiber.StatusBadRequest, "category icon too long (max 50 characters)")
	}
	if mc.AllocationPercent < 0 || mc.AllocationPercent > 100 {
		return fiber.NewError(fiber.StatusBadRequest, "category allocation_percent must be between 0 and 100")
	}
	return nil
}

// validateMigrateExpense checks migration expense fields.
func validateMigrateExpense(me MigrateExpense) error {
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
	return nil
}
