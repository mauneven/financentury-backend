package handlers

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

	// PERF: previously this went through DB.GetCtx, which wraps the query in
	// `SELECT json_agg(to_json(t)) FROM (...)` — that costs an extra server-
	// side JSON materialisation plus a second Unmarshal on our side. Direct
	// pgx Query scans straight into the struct and drops the byte round-trip
	// from Supabase (measurable egress win on large lists).
	// PERF: amount is NUMERIC — cast to float8 so pgx can scan into float64.
	// expense_date is DATE — cast to text so pgx can scan into string.
	rows, err := database.DB.Pool.Query(reqCtx, `
		SELECT id, budget_id, category_id, amount::float8, description,
		       to_char(expense_date, 'YYYY-MM-DD') AS expense_date,
		       created_by, created_at, updated_at
		FROM budget_expenses
		WHERE budget_id = $1
		  AND expense_date >= $2
		ORDER BY expense_date DESC
		LIMIT $3 OFFSET $4
	`, budgetID, expenseRetentionCutoff(), limit, offset)
	if err != nil {
		return errInternal(c, "failed to fetch expenses")
	}
	defer rows.Close()

	expenses := make([]models.Expense, 0)
	for rows.Next() {
		var e models.Expense
		if err := rows.Scan(&e.ID, &e.BudgetID, &e.CategoryID, &e.Amount,
			&e.Description, &e.ExpenseDate, &e.CreatedBy,
			&e.CreatedAt, &e.UpdatedAt); err != nil {
			return errInternal(c, "failed to parse expense row")
		}
		expenses = append(expenses, e)
	}
	if err := rows.Err(); err != nil {
		return errInternal(c, "failed to iterate expenses")
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
	// Use !(amount > 0) so NaN (which fails every comparison) is rejected too.
	if !(req.Amount > 0) {
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

	// PERF: fuse access check + expense-count cap + category-exists check
	// into a single round-trip. The previous code fired 3 separate SELECTs
	// (verifyBudgetAccess + COUNT(*) + EXISTS category) plus the INSERT —
	// 4 round-trips per typed expense. The merged SELECT returns three
	// booleans/counts so we can distinguish the failure modes and emit the
	// precise 400/404 the client expects.
	var (
		hasAccess       bool
		expenseCount    int
		categoryBelongs bool
	)
	if err := database.DB.Pool.QueryRow(reqCtx, `
		SELECT
			EXISTS(SELECT 1 FROM budgets WHERE id = $1 AND user_id = $2)
				OR EXISTS(SELECT 1 FROM budget_collaborators WHERE budget_id = $1 AND user_id = $2),
			(SELECT COUNT(*) FROM budget_expenses WHERE budget_id = $1),
			EXISTS(SELECT 1 FROM budget_categories WHERE id = $3 AND budget_id = $1)
	`, budgetID, userID, req.CategoryID).Scan(&hasAccess, &expenseCount, &categoryBelongs); err != nil {
		log.Printf("[createExpense] preflight failed budget=%s user=%s: %v", budgetID, userID, err)
		return errInternal(c, "failed to verify budget")
	}
	if !hasAccess {
		return errNotFound(c, "budget not found")
	}
	if expenseCount >= maxExpensesPerBudget {
		return errBadRequest(c, "expense limit reached for this budget")
	}
	if !categoryBelongs {
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

	// PERF: expense change invalidates any cached summary / trends for the
	// budget — counts, totals, and by-user breakdowns all shift. Any budget
	// linking this one also shows the new expense via linked_categories, so
	// invalidate those caches too.
	invalidateBudget(budgetID)
	invalidateLinkedTargets(budgetID)

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
	// Use !(amount > 0) so NaN (which fails every comparison) is rejected too.
	if req.Amount != nil && !(*req.Amount > 0) {
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

	// PERF: single-query preflight. Previously we ran
	//   1) fused access+expense SELECT     -> 1 round-trip
	//   2) if category_id: EXISTS category -> 1 round-trip
	//   3) UPDATE                          -> 1 round-trip
	// (3 round-trips). We now combine 1 and 2 — the category-belongs check
	// is a sub-select in the same statement gated on req.CategoryID being
	// non-nil. If the client didn't send a new category we pass NULL, the
	// sub-select short-circuits, and the extra check costs nothing.
	var (
		exp             models.Expense
		categoryBelongs bool
	)
	err := database.DB.Pool.QueryRow(reqCtx, `
		SELECT e.id, e.budget_id, e.category_id, e.amount::float8, e.description,
		       to_char(e.expense_date, 'YYYY-MM-DD') AS expense_date,
		       e.created_by, e.created_at, e.updated_at,
		       CASE
		         WHEN $4::uuid IS NULL THEN TRUE
		         ELSE EXISTS (
		           SELECT 1 FROM budget_categories
		            WHERE id = $4::uuid AND budget_id = $2
		         )
		       END
		FROM budget_expenses e
		WHERE e.id = $1 AND e.budget_id = $2
		  AND EXISTS (
		    SELECT 1 FROM budgets WHERE id = $2 AND user_id = $3
		    UNION ALL
		    SELECT 1 FROM budget_collaborators WHERE budget_id = $2 AND user_id = $3
		  )
	`, expenseID, budgetID, userID, req.CategoryID).Scan(
		&exp.ID, &exp.BudgetID, &exp.CategoryID, &exp.Amount, &exp.Description,
		&exp.ExpenseDate, &exp.CreatedBy, &exp.CreatedAt, &exp.UpdatedAt,
		&categoryBelongs,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "expense not found")
		}
		log.Printf("[updateExpense] scan failed exp=%s budget=%s user=%s: %v", expenseID, budgetID, userID, err)
		return errInternal(c, "failed to fetch expense")
	}
	if req.CategoryID != nil && !categoryBelongs {
		return errBadRequest(c, "category does not belong to this budget")
	}

	// Apply partial updates.
	if req.CategoryID != nil {
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

	// PERF: expense mutations invalidate every cached summary/trend/resume
	// view for the budget — and for any budget that links this one, whose
	// linked_categories view reflects the same underlying row.
	invalidateBudget(budgetID)
	invalidateLinkedTargets(budgetID)

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

	// PERF: already a single round-trip — DELETE...RETURNING with the access
	// check fused into the WHERE. No further reduction possible here.
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
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "expense not found")
		}
		log.Printf("[deleteExpense] exec failed exp=%s budget=%s user=%s: %v", expenseID, budgetID, userID, err)
		return errInternal(c, "failed to delete expense")
	}

	eid := deletedID.String()

	// PERF: expense mutation invalidates the budget-level cache and any
	// budget linking this one (their linked_categories view depends on us).
	invalidateBudget(budgetID)
	invalidateLinkedTargets(budgetID)

	broadcast(budgetID.String(), ws.MessageTypeExpenseDeleted, map[string]string{"id": eid})
	broadcastToLinkedTargets(budgetID, ws.MessageTypeExpenseDeleted, map[string]string{"id": eid})

	return c.SendStatus(fiber.StatusNoContent)
}
