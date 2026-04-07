package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/middleware"
	"github.com/the-financial-workspace/backend/internal/models"
)

// ListExpenses returns all expenses for a budget.
func ListExpenses(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	query := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("expense_date", "desc").
		Build()

	body, statusCode, err := database.DB.Get("budget_expenses", query)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch expenses"})
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to parse expenses"})
	}

	if expenses == nil {
		expenses = make([]models.Expense, 0)
	}

	return c.JSON(expenses)
}

// CreateExpense creates a new expense for a budget.
func CreateExpense(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}

	var req models.CreateExpenseRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid request body"})
	}

	if req.SubcategoryID == uuid.Nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "subcategory_id is required"})
	}
	if req.Amount <= 0 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "amount must be positive"})
	}
	if req.Amount > maxAmountValue {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "amount exceeds maximum allowed value"})
	}
	if len(req.Description) > maxDescriptionLength {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "description too long (max 1000 characters)"})
	}
	if req.ExpenseDate == "" {
		req.ExpenseDate = time.Now().UTC().Format("2006-01-02")
	}

	// Validate date format.
	if req.ExpenseDate != "" {
		if _, err := time.Parse("2006-01-02", req.ExpenseDate); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid date format, use YYYY-MM-DD"})
		}
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Verify subcategory belongs to this budget (via category).
	// Get the subcategory and check its category belongs to this budget.
	subQuery := database.NewFilter().
		Select("id,category_id").
		Eq("id", req.SubcategoryID.String()).
		Build()

	subBody, statusCode, err := database.DB.Get("budget_subcategories", subQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "subcategory does not belong to this budget"})
	}

	var subResults []struct {
		ID         string `json:"id"`
		CategoryID string `json:"category_id"`
	}
	if err := json.Unmarshal(subBody, &subResults); err != nil || len(subResults) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "subcategory does not belong to this budget"})
	}

	// Verify the category belongs to this budget.
	catCheckQuery := database.NewFilter().
		Select("id").
		Eq("id", subResults[0].CategoryID).
		Eq("budget_id", budgetID.String()).
		Build()

	catBody, statusCode, err := database.DB.Get("budget_categories", catCheckQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "subcategory does not belong to this budget"})
	}

	var catFound []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(catBody, &catFound); err != nil || len(catFound) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "subcategory does not belong to this budget"})
	}

	now := time.Now().UTC()
	expenseID := uuid.New()

	createdBy := userID
	expense := models.Expense{
		ID:            expenseID,
		BudgetID:      budgetID,
		SubcategoryID: req.SubcategoryID,
		Amount:        req.Amount,
		Description:   req.Description,
		ExpenseDate:   req.ExpenseDate,
		CreatedBy:     &createdBy,
		CreatedAt:     now,
	}

	payload := map[string]interface{}{
		"id":             expenseID.String(),
		"budget_id":      budgetID.String(),
		"subcategory_id": req.SubcategoryID.String(),
		"amount":         req.Amount,
		"description":    req.Description,
		"expense_date":   req.ExpenseDate,
		"created_by":     userID.String(),
		"created_at":     now.Format(time.RFC3339Nano),
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to serialize request"})
	}

	_, statusCode, err = database.DB.Post("budget_expenses", payloadBytes)
	if err != nil || statusCode != http.StatusCreated {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to create expense"})
	}

	return c.Status(fiber.StatusCreated).JSON(expense)
}

// UpdateExpense updates an existing expense.
func UpdateExpense(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}
	expenseID, err := uuid.Parse(c.Params("expenseId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid expense ID"})
	}

	var req models.UpdateExpenseRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid request body"})
	}

	// Validate fields if provided.
	if req.Amount != nil && *req.Amount > maxAmountValue {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "amount exceeds maximum allowed value"})
	}
	if req.Description != nil && len(*req.Description) > maxDescriptionLength {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "description too long (max 1000 characters)"})
	}
	if req.ExpenseDate != nil && *req.ExpenseDate != "" {
		if _, err := time.Parse("2006-01-02", *req.ExpenseDate); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid date format, use YYYY-MM-DD"})
		}
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Fetch existing expense.
	getQuery := database.NewFilter().
		Select("*").
		Eq("id", expenseID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_expenses", getQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to fetch expense"})
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil || len(expenses) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "expense not found"})
	}

	exp := expenses[0]

	// Apply partial updates.
	if req.SubcategoryID != nil {
		// Verify the new subcategory belongs to this budget.
		subQuery := database.NewFilter().
			Select("id,category_id").
			Eq("id", req.SubcategoryID.String()).
			Build()

		subBody, statusCode, err := database.DB.Get("budget_subcategories", subQuery)
		if err != nil || statusCode != http.StatusOK {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "subcategory does not belong to this budget"})
		}

		var subResults []struct {
			ID         string `json:"id"`
			CategoryID string `json:"category_id"`
		}
		if err := json.Unmarshal(subBody, &subResults); err != nil || len(subResults) == 0 {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "subcategory does not belong to this budget"})
		}

		catCheckQuery := database.NewFilter().
			Select("id").
			Eq("id", subResults[0].CategoryID).
			Eq("budget_id", budgetID.String()).
			Build()

		catBody, statusCode, err := database.DB.Get("budget_categories", catCheckQuery)
		if err != nil || statusCode != http.StatusOK {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "subcategory does not belong to this budget"})
		}

		var catFound []struct{ ID string `json:"id"` }
		if err := json.Unmarshal(catBody, &catFound); err != nil || len(catFound) == 0 {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "subcategory does not belong to this budget"})
		}

		exp.SubcategoryID = *req.SubcategoryID
	}
	if req.Amount != nil {
		if *req.Amount <= 0 {
			return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "amount must be positive"})
		}
		exp.Amount = *req.Amount
	}
	if req.Description != nil {
		exp.Description = *req.Description
	}
	if req.ExpenseDate != nil {
		exp.ExpenseDate = *req.ExpenseDate
	}

	updatePayload := map[string]interface{}{
		"subcategory_id": exp.SubcategoryID.String(),
		"amount":         exp.Amount,
		"description":    exp.Description,
		"expense_date":   exp.ExpenseDate,
	}
	updateBytes, err := json.Marshal(updatePayload)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to serialize request"})
	}

	patchQuery := database.NewFilter().
		Eq("id", expenseID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	_, statusCode, err = database.DB.Patch("budget_expenses", patchQuery, updateBytes)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to update expense"})
	}

	return c.JSON(exp)
}

// DeleteExpense deletes an expense.
func DeleteExpense(c *fiber.Ctx) error {
	userID := middleware.GetUserID(c)
	if userID == uuid.Nil {
		return c.Status(fiber.StatusUnauthorized).JSON(models.ErrorResponse{Error: "unauthorized"})
	}
	budgetID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid budget ID"})
	}
	expenseID, err := uuid.Parse(c.Params("expenseId"))
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(models.ErrorResponse{Error: "invalid expense ID"})
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "budget not found"})
	}

	// Verify expense exists.
	checkQuery := database.NewFilter().
		Select("id").
		Eq("id", expenseID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budget_expenses", checkQuery)
	if err != nil || statusCode != http.StatusOK {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to verify expense"})
	}

	var found []struct{ ID string `json:"id"` }
	if err := json.Unmarshal(body, &found); err != nil || len(found) == 0 {
		return c.Status(fiber.StatusNotFound).JSON(models.ErrorResponse{Error: "expense not found"})
	}

	// Delete the expense.
	delQuery := database.NewFilter().
		Eq("id", expenseID.String()).
		Eq("budget_id", budgetID.String()).
		Build()

	_, statusCode, err = database.DB.Delete("budget_expenses", delQuery)
	if err != nil || statusCode >= 300 {
		return c.Status(fiber.StatusInternalServerError).JSON(models.ErrorResponse{Error: "failed to delete expense"})
	}

	return c.SendStatus(fiber.StatusNoContent)
}
