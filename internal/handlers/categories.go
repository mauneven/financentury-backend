package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// maxCategoriesPerBudget mirrors the hard cap enforced at the DB level
// (see the enforce_budget_category_cap trigger in schema.sql). Keeping it
// in sync avoids needing a round-trip to hit the trigger error on happy paths.
const maxCategoriesPerBudget = 50

// ListCategories returns every category belonging to a budget, ordered by
// sort_order. Access is granted to owners and collaborators.
func ListCategories(c *fiber.Ctx) error {
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

	rows, err := database.DB.Pool.Query(reqCtx, `
		SELECT id, budget_id, name, allocation_value, icon, sort_order, created_at
		FROM budget_categories
		WHERE budget_id = $1
		ORDER BY sort_order ASC, created_at ASC
	`, budgetID)
	if err != nil {
		return errInternal(c, "failed to fetch categories")
	}
	defer rows.Close()

	categories := make([]models.Category, 0)
	for rows.Next() {
		var cat models.Category
		if err := rows.Scan(&cat.ID, &cat.BudgetID, &cat.Name, &cat.AllocationValue,
			&cat.Icon, &cat.SortOrder, &cat.CreatedAt); err != nil {
			return errInternal(c, "failed to parse category row")
		}
		categories = append(categories, cat)
	}
	if err := rows.Err(); err != nil {
		return errInternal(c, "failed to iterate categories")
	}

	return c.JSON(categories)
}

// CreateCategory creates a new flat category under a budget. Owner-only.
// Enforces the 50-per-budget cap and the budget-wide allocation ceiling
// (sum of allocations must not exceed the monthly income).
func CreateCategory(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	var req models.CreateCategoryRequest
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
	if req.AllocationValue < 0 {
		return errBadRequest(c, "allocation_value must be positive")
	}

	if err := verifyBudgetOwnership(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Validate and insert atomically: lock the budget row so concurrent
	// category creates on the same budget can't collectively break the
	// allocation or count ceilings.
	now := time.Now().UTC()
	catID := uuid.New()

	tx, err := database.DB.Pool.Begin(context.Background())
	if err != nil {
		return errInternal(c, "failed to start transaction")
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	var currentIncome float64
	if err := tx.QueryRow(context.Background(),
		`SELECT monthly_income FROM budgets WHERE id = $1 FOR UPDATE`, budgetID).Scan(&currentIncome); err != nil {
		return errInternal(c, "failed to fetch budget")
	}

	if req.AllocationValue > currentIncome {
		return errBadRequest(c, "allocation_value exceeds budget income")
	}

	// Check both the category count and the existing allocation total under
	// the lock — one round-trip covers both invariants.
	var categoryCount int
	var totalAlloc float64
	if err := tx.QueryRow(context.Background(),
		`SELECT COUNT(*), COALESCE(SUM(allocation_value), 0)
		 FROM budget_categories WHERE budget_id = $1`, budgetID).Scan(&categoryCount, &totalAlloc); err != nil {
		return errInternal(c, "failed to check existing categories")
	}

	if categoryCount >= maxCategoriesPerBudget {
		return errBadRequest(c, "maximum number of categories reached (50)")
	}
	if totalAlloc+req.AllocationValue > currentIncome {
		return errBadRequest(c, "total allocation would exceed budget income")
	}

	if _, err := tx.Exec(context.Background(),
		`INSERT INTO budget_categories (id, budget_id, name, allocation_value, icon, sort_order, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		catID, budgetID, req.Name, req.AllocationValue, req.Icon, req.SortOrder, now); err != nil {
		return errInternal(c, "failed to create category")
	}

	if err := tx.Commit(context.Background()); err != nil {
		return errInternal(c, "failed to commit category creation")
	}

	cat := models.Category{
		ID:              catID,
		BudgetID:        budgetID,
		Name:            req.Name,
		AllocationValue: req.AllocationValue,
		Icon:            req.Icon,
		SortOrder:       req.SortOrder,
		CreatedAt:       now,
	}

	broadcast(budgetID.String(), ws.MessageTypeCategoryCreated, cat)
	broadcastToLinkedTargets(budgetID, ws.MessageTypeCategoryCreated, cat)

	return c.Status(fiber.StatusCreated).JSON(cat)
}

// UpdateCategory updates an existing category. Owner-only.
func UpdateCategory(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	catID, ok := parseUUIDParam(c, "catId")
	if !ok {
		return errBadRequest(c, "invalid category ID")
	}

	var req models.UpdateCategoryRequest
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
	if req.AllocationValue != nil && *req.AllocationValue < 0 {
		return errBadRequest(c, "allocation_value must be positive")
	}

	if err := verifyBudgetOwnership(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// Fetch the existing category, verifying it belongs to the budget.
	var cat models.Category
	if err := database.DB.Pool.QueryRow(c.Context(), `
		SELECT id, budget_id, name, allocation_value, icon, sort_order, created_at
		FROM budget_categories
		WHERE id = $1 AND budget_id = $2
	`, catID, budgetID).Scan(
		&cat.ID, &cat.BudgetID, &cat.Name, &cat.AllocationValue, &cat.Icon,
		&cat.SortOrder, &cat.CreatedAt,
	); err != nil {
		return errNotFound(c, "category not found")
	}

	// Apply partial updates in memory first.
	if req.Name != nil {
		cat.Name = *req.Name
	}
	if req.AllocationValue != nil {
		cat.AllocationValue = *req.AllocationValue
	}
	if req.Icon != nil {
		cat.Icon = *req.Icon
	}
	if req.SortOrder != nil {
		cat.SortOrder = *req.SortOrder
	}

	// Persist inside a transaction that locks the budget row so concurrent
	// allocation changes on the same budget cannot collectively exceed income.
	tx, err := database.DB.Pool.Begin(context.Background())
	if err != nil {
		return errInternal(c, "failed to start transaction")
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	if req.AllocationValue != nil {
		var currentIncome float64
		if err := tx.QueryRow(context.Background(),
			`SELECT monthly_income FROM budgets WHERE id = $1 FOR UPDATE`, budgetID).Scan(&currentIncome); err != nil {
			return errInternal(c, "failed to fetch budget")
		}

		var otherTotal float64
		if err := tx.QueryRow(context.Background(),
			`SELECT COALESCE(SUM(allocation_value), 0) FROM budget_categories
			 WHERE budget_id = $1 AND id <> $2`, budgetID, catID).Scan(&otherTotal); err != nil {
			return errInternal(c, "failed to check existing allocations")
		}

		if otherTotal+*req.AllocationValue > currentIncome {
			return errBadRequest(c, "total allocation would exceed budget income")
		}
	}

	if _, err := tx.Exec(context.Background(),
		`UPDATE budget_categories
		 SET name = $1, allocation_value = $2, icon = $3, sort_order = $4
		 WHERE id = $5 AND budget_id = $6`,
		cat.Name, cat.AllocationValue, cat.Icon, cat.SortOrder,
		catID, budgetID); err != nil {
		return errInternal(c, "failed to update category")
	}

	if err := tx.Commit(context.Background()); err != nil {
		return errInternal(c, "failed to commit category update")
	}

	broadcast(budgetID.String(), ws.MessageTypeCategoryUpdated, cat)
	broadcastToLinkedTargets(budgetID, ws.MessageTypeCategoryUpdated, cat)

	return c.JSON(cat)
}

// DeleteCategory deletes a category and (via CASCADE) its expenses. Owner-only.
func DeleteCategory(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	catID, ok := parseUUIDParam(c, "catId")
	if !ok {
		return errBadRequest(c, "invalid category ID")
	}

	if err := verifyBudgetOwnership(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// DELETE ... RETURNING verifies existence + ownership + deletion in one
	// round-trip. budget_id is rechecked in the WHERE clause so an attacker
	// cannot target categories in a budget they own via a different budget ID.
	var deletedID uuid.UUID
	if err := database.DB.Pool.QueryRow(c.Context(), `
		DELETE FROM budget_categories
		WHERE id = $1 AND budget_id = $2
		RETURNING id
	`, catID, budgetID).Scan(&deletedID); err != nil {
		return errNotFound(c, "category not found")
	}

	cid := deletedID.String()

	broadcast(budgetID.String(), ws.MessageTypeCategoryDeleted, map[string]string{"id": cid})
	broadcastToLinkedTargets(budgetID, ws.MessageTypeCategoryDeleted, map[string]string{"id": cid})

	return c.SendStatus(fiber.StatusNoContent)
}
