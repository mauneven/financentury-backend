package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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

	// FIX: allocation_value is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	rows, err := database.DB.Pool.Query(reqCtx, `
		SELECT id, budget_id, name, allocation_value::float8, icon, sort_order, created_at
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

	// PERF: validate and insert atomically, combining ownership-verification
	// with the lock acquisition. The previous flow did
	//   1) SELECT EXISTS (ownership)  2) BEGIN  3) SELECT ... FOR UPDATE
	// which is now just
	//   1) BEGIN  2) SELECT monthly_income ... WHERE id=$1 AND user_id=$2 FOR UPDATE
	// saving one round-trip. If the budget doesn't belong to the user the
	// FOR UPDATE returns no rows and we 404 — same semantics as before.
	reqCtx := c.Context()
	now := time.Now().UTC()
	catID := uuid.New()

	tx, err := database.DB.Pool.Begin(reqCtx)
	if err != nil {
		return errInternal(c, "failed to start transaction")
	}
	defer tx.Rollback(reqCtx) //nolint:errcheck

	var currentIncome float64
	// FIX: monthly_income is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	if err := tx.QueryRow(reqCtx,
		`SELECT monthly_income::float8 FROM budgets WHERE id = $1 AND user_id = $2 FOR UPDATE`,
		budgetID, userID).Scan(&currentIncome); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "budget not found")
		}
		return errInternal(c, "failed to fetch budget")
	}

	if req.AllocationValue > currentIncome {
		return errBadRequest(c, "allocation_value exceeds budget income")
	}

	// Check the category count, existing allocation total, and current max
	// sort_order under the lock — one round-trip covers all three invariants.
	var categoryCount int
	var totalAlloc float64
	var maxSortOrder int
	// FIX: SUM(allocation_value) is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	if err := tx.QueryRow(reqCtx,
		`SELECT COUNT(*), COALESCE(SUM(allocation_value), 0)::float8, COALESCE(MAX(sort_order), 0)
		 FROM budget_categories WHERE budget_id = $1`, budgetID).Scan(&categoryCount, &totalAlloc, &maxSortOrder); err != nil {
		return errInternal(c, "failed to check existing categories")
	}

	if categoryCount >= maxCategoriesPerBudget {
		return errBadRequest(c, "maximum number of categories reached (50)")
	}
	if totalAlloc+req.AllocationValue > currentIncome {
		return errBadRequest(c, "total allocation would exceed budget income")
	}

	// When the client omits sort_order (or sends 0), append the new category
	// at the end so freshly-created categories don't all collide at position 0.
	sortOrder := req.SortOrder
	if sortOrder <= 0 {
		sortOrder = maxSortOrder + 1
	}

	if _, err := tx.Exec(reqCtx,
		`INSERT INTO budget_categories (id, budget_id, name, allocation_value, icon, sort_order, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		catID, budgetID, req.Name, req.AllocationValue, req.Icon, sortOrder, now); err != nil {
		return errInternal(c, "failed to create category")
	}

	if err := tx.Commit(reqCtx); err != nil {
		return errInternal(c, "failed to commit category creation")
	}

	// PERF: category changes invalidate the summary (allocations change).
	// Linked targets also see this category in their linked_categories view,
	// so purge their caches too.
	invalidateBudget(budgetID)
	invalidateLinkedTargets(budgetID)

	cat := models.Category{
		ID:              catID,
		BudgetID:        budgetID,
		Name:            req.Name,
		AllocationValue: req.AllocationValue,
		Icon:            req.Icon,
		SortOrder:       sortOrder,
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

	// PERF: hot path (PATCH from the slider fires rapidly). The old flow ran
	//   verifyBudgetOwnership + SELECT category + BEGIN + (if amount)
	//   SELECT monthly_income FOR UPDATE + SELECT SUM(other allocations)
	//   + UPDATE + COMMIT
	// i.e. 5-7 round-trips. Now we fuse ownership verification and the
	// income-lock + other-total check into a single query, and use
	// UPDATE ... RETURNING so we don't need an initial SELECT of the
	// category row. Fast path (no allocation change) drops from 3 to 1 query.
	reqCtx := c.Context()

	// Fast path: no allocation change. Single UPDATE with ownership fused
	// into the WHERE clause via a sub-select on budgets(user_id).
	if req.AllocationValue == nil {
		var cat models.Category
		// FIX: allocation_value is NUMERIC — pgx binary format can't scan NUMERIC→float64
		// without ::float8 cast.
		err := database.DB.Pool.QueryRow(reqCtx, `
			UPDATE budget_categories
			SET name = COALESCE($1, name),
			    icon = COALESCE($2, icon),
			    sort_order = COALESCE($3, sort_order)
			WHERE id = $4 AND budget_id = $5
			  AND EXISTS (SELECT 1 FROM budgets WHERE id = $5 AND user_id = $6)
			RETURNING id, budget_id, name, allocation_value::float8, icon, sort_order, created_at
		`, req.Name, req.Icon, req.SortOrder, catID, budgetID, userID).Scan(
			&cat.ID, &cat.BudgetID, &cat.Name, &cat.AllocationValue, &cat.Icon,
			&cat.SortOrder, &cat.CreatedAt,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errNotFound(c, "category not found")
			}
			return errInternal(c, "failed to update category")
		}

		invalidateBudget(budgetID)
		invalidateLinkedTargets(budgetID)
		broadcast(budgetID.String(), ws.MessageTypeCategoryUpdated, cat)
		broadcastToLinkedTargets(budgetID, ws.MessageTypeCategoryUpdated, cat)
		return c.JSON(cat)
	}

	// Slow path: allocation change requires the budget-row lock so concurrent
	// allocation writers serialize on us. Ownership is fused into the lock
	// SELECT, and the OTHER-allocations SUM is computed in the same query
	// (saves one round-trip vs the previous separate SELECT).
	tx, err := database.DB.Pool.Begin(reqCtx)
	if err != nil {
		return errInternal(c, "failed to start transaction")
	}
	defer tx.Rollback(reqCtx) //nolint:errcheck

	var currentIncome, otherTotal float64
	// FIX: monthly_income and SUM(allocation_value) are NUMERIC — pgx binary format
	// can't scan NUMERIC→float64 without ::float8 casts.
	if err := tx.QueryRow(reqCtx, `
		SELECT b.monthly_income::float8,
		       COALESCE((SELECT SUM(allocation_value)
		                   FROM budget_categories
		                  WHERE budget_id = $1 AND id <> $2), 0)::float8
		FROM budgets b
		WHERE b.id = $1 AND b.user_id = $3
		FOR UPDATE OF b
	`, budgetID, catID, userID).Scan(&currentIncome, &otherTotal); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "budget not found")
		}
		return errInternal(c, "failed to fetch budget")
	}

	if otherTotal+*req.AllocationValue > currentIncome {
		return errBadRequest(c, "total allocation would exceed budget income")
	}

	// UPDATE ... RETURNING gives us the fresh row in one round-trip. COALESCE
	// applies partial updates directly in SQL so we don't need to pre-fetch.
	var cat models.Category
	// FIX: allocation_value is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	if err := tx.QueryRow(reqCtx, `
		UPDATE budget_categories
		SET name = COALESCE($1, name),
		    allocation_value = $2,
		    icon = COALESCE($3, icon),
		    sort_order = COALESCE($4, sort_order)
		WHERE id = $5 AND budget_id = $6
		RETURNING id, budget_id, name, allocation_value::float8, icon, sort_order, created_at
	`, req.Name, *req.AllocationValue, req.Icon, req.SortOrder, catID, budgetID).Scan(
		&cat.ID, &cat.BudgetID, &cat.Name, &cat.AllocationValue, &cat.Icon,
		&cat.SortOrder, &cat.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "category not found")
		}
		return errInternal(c, "failed to update category")
	}

	if err := tx.Commit(reqCtx); err != nil {
		return errInternal(c, "failed to commit category update")
	}

	invalidateBudget(budgetID)
	invalidateLinkedTargets(budgetID)
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

	// PERF: fused ownership + delete. The previous version did a separate
	// verifyBudgetOwnership SELECT before the DELETE...RETURNING; now a
	// single DELETE with EXISTS(budgets WHERE user_id=...) enforces both
	// invariants in one round-trip. No rows -> 404.
	var deletedID uuid.UUID
	if err := database.DB.Pool.QueryRow(c.Context(), `
		DELETE FROM budget_categories
		WHERE id = $1 AND budget_id = $2
		  AND EXISTS (SELECT 1 FROM budgets WHERE id = $2 AND user_id = $3)
		RETURNING id
	`, catID, budgetID, userID).Scan(&deletedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "category not found")
		}
		return errInternal(c, "failed to delete category")
	}

	cid := deletedID.String()

	invalidateBudget(budgetID)
	invalidateLinkedTargets(budgetID)

	broadcast(budgetID.String(), ws.MessageTypeCategoryDeleted, map[string]string{"id": cid})
	broadcastToLinkedTargets(budgetID, ws.MessageTypeCategoryDeleted, map[string]string{"id": cid})

	return c.SendStatus(fiber.StatusNoContent)
}
