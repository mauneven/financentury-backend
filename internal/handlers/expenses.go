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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	query := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("expense_date", "desc").
		Build()

	body, statusCode, err := database.DB.Get("budget_expenses", query)
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

	var req models.CreateExpenseRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Validate required fields.
	if req.CategoryID == uuid.Nil {
		return errBadRequest(c, "subcategory_id is required")
	}
	if req.Amount <= 0 {
		return errBadRequest(c, "amount must be positive")
	}
	if req.Amount > maxAmountValue {
		return errBadRequest(c, "amount exceeds maximum allowed value")
	}
	if len(req.Description) > maxDescriptionLength {
		return errBadRequest(c, "description too long (max 1000 characters)")
	}
	if req.ExpenseDate == "" {
		req.ExpenseDate = time.Now().UTC().Format(dateFormat)
	}
	if !isValidDate(req.ExpenseDate) {
		return errBadRequest(c, "invalid date format, use YYYY-MM-DD")
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Verify category belongs to this budget.
	if err := verifyCategoryBelongsToBudget(req.CategoryID, budgetID); err != nil {
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

	payload := map[string]interface{}{
		"id":             expenseID.String(),
		"budget_id":      budgetID.String(),
		"subcategory_id": req.CategoryID.String(),
		"amount":         req.Amount,
		"description":    req.Description,
		"expense_date":   req.ExpenseDate,
		"created_by":     userID.String(),
		"created_at":     now.Format(time.RFC3339Nano),
		"updated_at":     now.Format(time.RFC3339Nano),
	}
	payloadBytes, err := marshalJSON(payload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	_, statusCode, err := database.DB.Post("budget_expenses", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		return errInternal(c, "failed to create expense")
	}

	broadcast(budgetID.String(), ws.MessageTypeExpenseCreated, expense)

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

	// Validate optional fields.
	if req.Amount != nil && *req.Amount <= 0 {
		return errBadRequest(c, "amount must be positive")
	}
	if req.Amount != nil && *req.Amount > maxAmountValue {
		return errBadRequest(c, "amount exceeds maximum allowed value")
	}
	if req.Description != nil && len(*req.Description) > maxDescriptionLength {
		return errBadRequest(c, "description too long (max 1000 characters)")
	}
	if req.ExpenseDate != nil {
		if *req.ExpenseDate == "" {
			return errBadRequest(c, "expense_date cannot be empty")
		}
		if !isValidDate(*req.ExpenseDate) {
			return errBadRequest(c, "invalid date format, use YYYY-MM-DD")
		}
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Fetch existing expense.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", expenseID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_expenses", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to fetch expense")
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil || len(expenses) == 0 {
		return errNotFound(c, "expense not found")
	}

	exp := expenses[0]

	// Apply partial updates.
	if req.CategoryID != nil {
		if err := verifyCategoryBelongsToBudget(*req.CategoryID, budgetID); err != nil {
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

	updatePayload := map[string]interface{}{
		"subcategory_id": exp.CategoryID.String(),
		"amount":         exp.Amount,
		"description":    exp.Description,
		"expense_date":   exp.ExpenseDate,
		"updated_at":     now.Format(time.RFC3339Nano),
	}
	updateBytes, err := marshalJSON(updatePayload)
	if err != nil {
		return errInternal(c, "failed to serialize request")
	}

	patchQuery := database.NewFilter().
		Eq("id", expenseID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budget_expenses", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to update expense")
	}

	broadcast(budgetID.String(), ws.MessageTypeExpenseUpdated, exp)

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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	eid := expenseID.String()

	// Verify expense exists.
	checkQuery := database.NewFilter().
		Select("id").
		Eq("id", eid).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_expenses", checkQuery)
	if err != nil || statusCode != http.StatusOK {
		return errInternal(c, "failed to verify expense")
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return errNotFound(c, "expense not found")
	}

	// Delete the expense.
	delQuery := database.NewFilter().
		Eq("id", eid).
		Eq("budget_id", budgetID.String()).
		Build()

	_, statusCode, err = database.DB.Delete("budget_expenses", delQuery)
	if err != nil || statusCode >= 300 {
		return errInternal(c, "failed to delete expense")
	}

	broadcast(budgetID.String(), ws.MessageTypeExpenseDeleted, map[string]string{"id": eid})

	return c.SendStatus(fiber.StatusNoContent)
}
