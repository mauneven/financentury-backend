package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// expenseRetentionCutoff returns the cutoff date (12 months ago) as a
// YYYY-MM-DD string. Expenses with expense_date before this value are eligible
// for automatic deletion.
func expenseRetentionCutoff() string {
	return time.Now().UTC().AddDate(-1, 0, 0).Format("2006-01-02")
}

// pruneOldExpenses deletes expenses older than 12 months for the given budget.
// Errors are logged but not returned — this is a best-effort cleanup that must
// not block or fail the calling request.
func pruneOldExpenses(budgetID uuid.UUID) {
	cutoff := expenseRetentionCutoff()
	query := database.NewFilter().
		Eq("budget_id", budgetID.String()).
		Lt("expense_date", cutoff).
		Build()
	_, _, err := database.DB.Delete("budget_expenses", query)
	if err != nil {
		log.Printf("[expenses] prune old expenses for budget %s: %v", budgetID, err)
	}
}

// StartExpensePruner runs expense pruning every hour in the background.
func StartExpensePruner() {
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			pruneAllOldExpenses()
		}
	}()
}

// pruneAllOldExpenses prunes old expenses across all budgets.
func pruneAllOldExpenses() {
	_, err := database.DB.Pool.Exec(context.Background(),
		`DELETE FROM budget_expenses
		 WHERE expense_date < (CURRENT_DATE - INTERVAL '24 months')`)
	if err != nil {
		log.Printf("[prune] failed to prune old expenses: %v", err)
	}
}

// parsePaginationParams extracts limit and offset from query params with
// sensible defaults (limit=100, offset=0) and a hard ceiling (limit<=500).
func parsePaginationParams(c *fiber.Ctx) (limit, offset int) {
	const defaultLimit = 100
	const maxLimit = 500

	limit = defaultLimit
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	offset = 0
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// ListExpenses returns all expenses for a budget, ordered by date descending.
func ListExpenses(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	reqCtx := c.Context()
	if err := verifyBudgetAccessCtx(reqCtx, budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	limit, offset := parsePaginationParams(c)

	query := database.NewFilter().
		Select("id,budget_id,category_id,amount,description,expense_date,created_by,created_at,updated_at").
		Eq("budget_id", budgetID.String()).
		Gte("expense_date", expenseRetentionCutoff()).
		Order("expense_date", "desc").
		Limit(limit).
		Offset(offset).
		Build()

	body, statusCode, err := database.DB.GetCtx(reqCtx, "budget_expenses", query)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch expenses")
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return errInternal(c, "failed to parse expenses")
	}

	if expenses == nil {
		expenses = make([]models.Expense, 0)
	}

	return c.JSON(expenses)
}

// maxExpensesPerBudget is the maximum number of expenses allowed in a single budget.
const maxExpensesPerBudget = 3000

// CreateExpense creates a new expense for a budget.
// On success it broadcasts an expense_created event via WebSocket.
func CreateExpense(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	reqCtx := c.Context()

	// Verify access before any queries to prevent unauthenticated probing.
	if err := verifyBudgetAccessCtx(reqCtx, budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Enforce per-budget expense limit using COUNT(*) instead of fetching all rows.
	var expenseCount int
	err := database.DB.Pool.QueryRow(reqCtx,
		`SELECT COUNT(*) FROM budget_expenses WHERE budget_id = $1`, budgetID).Scan(&expenseCount)
	if err != nil {
		return errInternal(c, "failed to check expense count")
	}
	if expenseCount >= maxExpensesPerBudget {
		return errBadRequest(c, "expense limit reached for this budget")
	}

	var req models.CreateExpenseRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Sanitize text inputs.
	req.Description = strings.TrimSpace(req.Description)
	req.ExpenseDate = strings.TrimSpace(req.ExpenseDate)

	// Validate required fields.
	if req.CategoryID == uuid.Nil {
		return errBadRequest(c, "category_id is required")
	}
	if req.Amount <= 0 {
		return errBadRequest(c, "amount must be positive")
	}
	if req.Amount > maxAmountValue {
		return errBadRequest(c, "amount exceeds maximum allowed value")
	}
	if len(req.Description) > maxDescriptionLength {
		return errBadRequest(c, "description too long (max 500 characters)")
	}
	if req.ExpenseDate == "" {
		req.ExpenseDate = time.Now().UTC().Format(dateFormat)
	}
	if !isValidDate(req.ExpenseDate) {
		return errBadRequest(c, "invalid date format, use YYYY-MM-DD")
	}
	if isDateTooFarInFuture(req.ExpenseDate) {
		return errBadRequest(c, "expense_date cannot be more than 1 year in the future")
	}

	// Flat category model: single existence check.
	var belongs bool
	if err := database.DB.Pool.QueryRow(reqCtx,
		`SELECT EXISTS(SELECT 1 FROM budget_categories WHERE id = $1 AND budget_id = $2)`,
		req.CategoryID, budgetID).Scan(&belongs); err != nil || !belongs {
		return errBadRequest(c, "category does not belong to this budget")
	}

	now := time.Now().UTC()
	expenseID := uuid.New()
	createdBy := userID

	expense := models.Expense{
		ID:          expenseID,
		BudgetID:    budgetID,
		CategoryID:  req.CategoryID,
		Amount:      req.Amount,
		Description: req.Description,
		ExpenseDate: req.ExpenseDate,
		CreatedBy:   &createdBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Direct INSERT via pool avoids the HTTP-layer JSON round-trip of DB.Post.
	if _, err := database.DB.Pool.Exec(reqCtx, `
		INSERT INTO budget_expenses
			(id, budget_id, category_id, amount, description, expense_date, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
	`, expenseID, budgetID, req.CategoryID, req.Amount, req.Description,
		req.ExpenseDate, userID, now); err != nil {
		return errInternal(c, "failed to create expense")
	}

	broadcast(budgetID.String(), ws.MessageTypeExpenseCreated, expense)
	broadcastToLinkedTargets(budgetID, ws.MessageTypeExpenseCreated, expense)

	return c.Status(fiber.StatusCreated).JSON(expense)
}

// UpdateExpense updates an existing expense.
// On success it broadcasts an expense_updated event via WebSocket.
func UpdateExpense(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	expenseID, ok := parseUUIDParam(c, "expenseId")
	if !ok {
		return errBadRequest(c, "invalid expense ID")
	}

	var req models.UpdateExpenseRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Sanitize text inputs.
	if req.Description != nil {
		trimmed := strings.TrimSpace(*req.Description)
		req.Description = &trimmed
	}
	if req.ExpenseDate != nil {
		trimmed := strings.TrimSpace(*req.ExpenseDate)
		req.ExpenseDate = &trimmed
	}

	// Validate optional fields.
	if req.Amount != nil && *req.Amount <= 0 {
		return errBadRequest(c, "amount must be positive")
	}
	if req.Amount != nil && *req.Amount > maxAmountValue {
		return errBadRequest(c, "amount exceeds maximum allowed value")
	}
	if req.Description != nil && len(*req.Description) > maxDescriptionLength {
		return errBadRequest(c, "description too long (max 500 characters)")
	}
	if req.ExpenseDate != nil {
		if *req.ExpenseDate == "" {
			return errBadRequest(c, "expense_date cannot be empty")
		}
		if !isValidDate(*req.ExpenseDate) {
			return errBadRequest(c, "invalid date format, use YYYY-MM-DD")
		}
		if isDateTooFarInFuture(*req.ExpenseDate) {
			return errBadRequest(c, "expense_date cannot be more than 1 year in the future")
		}
	}

	reqCtx := c.Context()

	// Fused access-check + expense fetch. A single query enforces:
	//   1. The expense belongs to the budget.
	//   2. The user owns or collaborates on the budget.
	var exp models.Expense
	err := database.DB.Pool.QueryRow(reqCtx, `
		SELECT e.id, e.budget_id, e.category_id, e.amount, e.description,
		       e.expense_date, e.created_by, e.created_at, e.updated_at
		FROM budget_expenses e
		WHERE e.id = $1 AND e.budget_id = $2
		  AND EXISTS (
		    SELECT 1 FROM budgets WHERE id = $2 AND user_id = $3
		    UNION ALL
		    SELECT 1 FROM budget_collaborators WHERE budget_id = $2 AND user_id = $3
		  )
	`, expenseID, budgetID, userID).Scan(
		&exp.ID, &exp.BudgetID, &exp.CategoryID, &exp.Amount, &exp.Description,
		&exp.ExpenseDate, &exp.CreatedBy, &exp.CreatedAt, &exp.UpdatedAt,
	)
	if err != nil {
		return errNotFound(c, "expense not found")
	}

	// Apply partial updates.
	if req.CategoryID != nil {
		var belongs bool
		if err := database.DB.Pool.QueryRow(reqCtx,
			`SELECT EXISTS(SELECT 1 FROM budget_categories WHERE id = $1 AND budget_id = $2)`,
			*req.CategoryID, budgetID).Scan(&belongs); err != nil || !belongs {
			return errBadRequest(c, "category does not belong to this budget")
		}
		exp.CategoryID = *req.CategoryID
	}
	if req.Amount != nil {
		exp.Amount = *req.Amount
	}
	if req.Description != nil {
		exp.Description = *req.Description
	}
	if req.ExpenseDate != nil {
		exp.ExpenseDate = *req.ExpenseDate
	}

	now := time.Now().UTC()
	exp.UpdatedAt = now

	if _, err := database.DB.Pool.Exec(reqCtx, `
		UPDATE budget_expenses
		SET category_id = $1, amount = $2, description = $3,
		    expense_date = $4, updated_at = $5
		WHERE id = $6 AND budget_id = $7
	`, exp.CategoryID, exp.Amount, exp.Description, exp.ExpenseDate, now,
		expenseID, budgetID); err != nil {
		return errInternal(c, "failed to update expense")
	}

	broadcast(budgetID.String(), ws.MessageTypeExpenseUpdated, exp)
	broadcastToLinkedTargets(budgetID, ws.MessageTypeExpenseUpdated, exp)

	return c.JSON(exp)
}

// DeleteExpense deletes an expense.
// On success it broadcasts an expense_deleted event via WebSocket.
func DeleteExpense(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	expenseID, ok := parseUUIDParam(c, "expenseId")
	if !ok {
		return errBadRequest(c, "invalid expense ID")
	}

	reqCtx := c.Context()

	// Single query: deletes only if the budget is accessible to the user
	// AND the expense belongs to that budget. RETURNING lets us detect a
	// non-existent expense without a separate pre-check GET.
	var deletedID uuid.UUID
	err := database.DB.Pool.QueryRow(reqCtx, `
		DELETE FROM budget_expenses
		WHERE id = $1 AND budget_id = $2
		  AND EXISTS (
		    SELECT 1 FROM budgets WHERE id = $2 AND user_id = $3
		    UNION ALL
		    SELECT 1 FROM budget_collaborators WHERE budget_id = $2 AND user_id = $3
		  )
		RETURNING id
	`, expenseID, budgetID, userID).Scan(&deletedID)
	if err != nil {
		return errNotFound(c, "expense not found")
	}

	eid := deletedID.String()

	broadcast(budgetID.String(), ws.MessageTypeExpenseDeleted, map[string]string{"id": eid})
	broadcastToLinkedTargets(budgetID, ws.MessageTypeExpenseDeleted, map[string]string{"id": eid})

	return c.SendStatus(fiber.StatusNoContent)
}
