package handlers

import (
	"context"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"github.com/the-financial-workspace/backend/internal/ws"
)

const maxLinksPerBudget = 10

// ListLinks returns all budget links for the given target budget.
func ListLinks(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget id")
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := database.DB.Pool.Query(ctx, `
		SELECT id, source_budget_id, target_budget_id, source_section_id,
		       source_category_id, target_section_id, filter_mode, created_by, created_at
		FROM budget_links
		WHERE target_budget_id = $1
		ORDER BY created_at
	`, budgetID)
	if err != nil {
		return errInternal(c, "failed to fetch links")
	}
	defer rows.Close()

	links := make([]models.BudgetLink, 0)
	for rows.Next() {
		var l models.BudgetLink
		if err := rows.Scan(&l.ID, &l.SourceBudgetID, &l.TargetBudgetID,
			&l.SourceSectionID, &l.SourceCategoryID, &l.TargetSectionID,
			&l.FilterMode, &l.CreatedBy, &l.CreatedAt); err != nil {
			return errInternal(c, "failed to parse link")
		}
		links = append(links, l)
	}

	return c.JSON(links)
}

// CreateLink creates a new budget link from a source section/category to the target budget.
func CreateLink(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget id")
	}

	var req struct {
		SourceBudgetID   uuid.UUID  `json:"source_budget_id"`
		SourceSectionID  uuid.UUID  `json:"source_section_id"`
		SourceCategoryID *uuid.UUID `json:"source_category_id,omitempty"`
		TargetSectionID  *uuid.UUID `json:"target_section_id,omitempty"`
		FilterMode       string     `json:"filter_mode"`
	}
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Validate filter mode.
	if req.FilterMode != "all" && req.FilterMode != "mine" {
		return errBadRequest(c, "filter_mode must be 'all' or 'mine'")
	}

	// No self-linking.
	if req.SourceBudgetID == budgetID {
		return errBadRequest(c, "cannot link a budget to itself")
	}

	// User must have access to both budgets.
	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "target budget not found")
	}
	if err := verifyBudgetAccess(req.SourceBudgetID, userID); err != nil {
		return errNotFound(c, "source budget not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Verify same currency.
	var targetCurrency, sourceCurrency string
	err := database.DB.Pool.QueryRow(ctx,
		"SELECT currency FROM budgets WHERE id = $1", budgetID).Scan(&targetCurrency)
	if err != nil {
		return errInternal(c, "failed to fetch target budget")
	}
	err = database.DB.Pool.QueryRow(ctx,
		"SELECT currency FROM budgets WHERE id = $1", req.SourceBudgetID).Scan(&sourceCurrency)
	if err != nil {
		return errInternal(c, "failed to fetch source budget")
	}
	if targetCurrency != sourceCurrency {
		return errBadRequest(c, "budgets must have the same currency")
	}

	// Verify source section exists in source budget.
	var sectionExists bool
	err = database.DB.Pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM budget_categories WHERE id = $1 AND budget_id = $2)",
		req.SourceSectionID, req.SourceBudgetID).Scan(&sectionExists)
	if err != nil || !sectionExists {
		return errNotFound(c, "source section not found in source budget")
	}

	// If category specified, verify it belongs to the section.
	if req.SourceCategoryID != nil {
		var catExists bool
		err = database.DB.Pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM budget_subcategories WHERE id = $1 AND category_id = $2)",
			*req.SourceCategoryID, req.SourceSectionID).Scan(&catExists)
		if err != nil || !catExists {
			return errNotFound(c, "source category not found in source section")
		}

		// Category links require target_section_id (which section in target budget to place it in).
		if req.TargetSectionID == nil {
			return errBadRequest(c, "target_section_id is required for category links")
		}
		var targetSectionExists bool
		err = database.DB.Pool.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM budget_categories WHERE id = $1 AND budget_id = $2)",
			*req.TargetSectionID, budgetID).Scan(&targetSectionExists)
		if err != nil || !targetSectionExists {
			return errNotFound(c, "target section not found in target budget")
		}
	}

	// Check mutual exclusivity: full-section link and single-category links can't coexist.
	if req.SourceCategoryID == nil {
		// Creating full-section link: reject if any category-level link exists for this section.
		var hasPartial bool
		err = database.DB.Pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM budget_links
			 WHERE target_budget_id = $1 AND source_section_id = $2
			   AND source_category_id IS NOT NULL)`,
			budgetID, req.SourceSectionID).Scan(&hasPartial)
		if err == nil && hasPartial {
			return errBadRequest(c, "cannot link entire section when individual categories from it are already linked")
		}
	} else {
		// Creating category link: reject if a full-section link already exists.
		var hasFull bool
		err = database.DB.Pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM budget_links
			 WHERE target_budget_id = $1 AND source_section_id = $2
			   AND source_category_id IS NULL)`,
			budgetID, req.SourceSectionID).Scan(&hasFull)
		if err == nil && hasFull {
			return errBadRequest(c, "cannot link individual category when the entire section is already linked")
		}
	}

	// Enforce per-budget link limit.
	var linkCount int
	err = database.DB.Pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM budget_links WHERE target_budget_id = $1", budgetID).Scan(&linkCount)
	if err == nil && linkCount >= maxLinksPerBudget {
		return errBadRequest(c, "maximum number of links reached")
	}

	// Prevent circular links (A→B and B→A).
	var reverseExists bool
	err = database.DB.Pool.QueryRow(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM budget_links
			WHERE source_budget_id = $1 AND target_budget_id = $2
		)`, budgetID, req.SourceBudgetID).Scan(&reverseExists)
	if err != nil {
		return errInternal(c, "failed to check for circular links")
	}
	if reverseExists {
		return errBadRequest(c, "cannot create circular link between budgets")
	}

	// Insert the link.
	var link models.BudgetLink
	err = database.DB.Pool.QueryRow(ctx, `
		INSERT INTO budget_links (source_budget_id, target_budget_id, source_section_id, source_category_id, target_section_id, filter_mode, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, source_budget_id, target_budget_id, source_section_id, source_category_id, target_section_id, filter_mode, created_by, created_at
	`, req.SourceBudgetID, budgetID, req.SourceSectionID, req.SourceCategoryID,
		req.TargetSectionID, req.FilterMode, userID,
	).Scan(&link.ID, &link.SourceBudgetID, &link.TargetBudgetID,
		&link.SourceSectionID, &link.SourceCategoryID, &link.TargetSectionID,
		&link.FilterMode, &link.CreatedBy, &link.CreatedAt)
	if err != nil {
		log.Printf("[links] insert failed: %v", err)
		return errInternal(c, "failed to create link")
	}

	broadcast(budgetID.String(), ws.MessageTypeLinkCreated, link)
	broadcast(req.SourceBudgetID.String(), ws.MessageTypeLinkCreated, link)

	return c.Status(fiber.StatusCreated).JSON(link)
}

// UpdateLink updates the filter_mode of a budget link.
func UpdateLink(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget id")
	}

	linkID, ok := parseUUIDParam(c, "linkId")
	if !ok {
		return errBadRequest(c, "invalid link id")
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	var req struct {
		FilterMode string `json:"filter_mode"`
	}
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}
	if req.FilterMode != "all" && req.FilterMode != "mine" {
		return errBadRequest(c, "filter_mode must be 'all' or 'mine'")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var link models.BudgetLink
	err := database.DB.Pool.QueryRow(ctx, `
		UPDATE budget_links SET filter_mode = $1
		WHERE id = $2 AND target_budget_id = $3
		RETURNING id, source_budget_id, target_budget_id, source_section_id, source_category_id, target_section_id, filter_mode, created_by, created_at
	`, req.FilterMode, linkID, budgetID,
	).Scan(&link.ID, &link.SourceBudgetID, &link.TargetBudgetID,
		&link.SourceSectionID, &link.SourceCategoryID, &link.TargetSectionID,
		&link.FilterMode, &link.CreatedBy, &link.CreatedAt)
	if err != nil {
		return errNotFound(c, "link not found")
	}

	broadcast(budgetID.String(), ws.MessageTypeLinkUpdated, link)

	return c.JSON(link)
}

// DeleteLink removes a budget link.
func DeleteLink(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget id")
	}

	linkID, ok := parseUUIDParam(c, "linkId")
	if !ok {
		return errBadRequest(c, "invalid link id")
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Fetch source_budget_id before deleting for WS notification.
	var sourceBudgetID uuid.UUID
	err := database.DB.Pool.QueryRow(ctx,
		"SELECT source_budget_id FROM budget_links WHERE id = $1 AND target_budget_id = $2",
		linkID, budgetID).Scan(&sourceBudgetID)
	if err != nil {
		return errNotFound(c, "link not found")
	}

	_, err = database.DB.Pool.Exec(ctx,
		"DELETE FROM budget_links WHERE id = $1 AND target_budget_id = $2",
		linkID, budgetID)
	if err != nil {
		return errInternal(c, "failed to delete link")
	}

	broadcast(budgetID.String(), ws.MessageTypeLinkDeleted, fiber.Map{"id": linkID})
	broadcast(sourceBudgetID.String(), ws.MessageTypeLinkDeleted, fiber.Map{"id": linkID})

	return c.SendStatus(fiber.StatusNoContent)
}

// GetLinkableBudgets returns budgets the user has access to (excluding the current one)
// that share the same currency, with their sections and categories.
func GetLinkableBudgets(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget id")
	}

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get current budget's currency.
	var currency string
	err := database.DB.Pool.QueryRow(ctx,
		"SELECT currency FROM budgets WHERE id = $1", budgetID).Scan(&currency)
	if err != nil {
		return errInternal(c, "failed to fetch budget")
	}

	// Fetch all other budgets the user has access to with the same currency.
	rows, err := database.DB.Pool.Query(ctx, `
		SELECT id, user_id, name, icon, monthly_income, currency,
		       billing_period_months, billing_cutoff_day, mode, created_at, updated_at
		FROM budgets
		WHERE currency = $1 AND id != $2
		  AND (user_id = $3 OR id IN (
		    SELECT budget_id FROM budget_collaborators WHERE user_id = $3
		  ))
		ORDER BY name
	`, currency, budgetID, userID)
	if err != nil {
		return errInternal(c, "failed to fetch linkable budgets")
	}
	defer rows.Close()

	type linkableBudget struct {
		models.Budget
		Sections []sectionWithCategories `json:"sections"`
	}
	type sectionEntry struct {
		b      models.Budget
		idx    int
	}

	var budgets []linkableBudget
	budgetIDs := make([]uuid.UUID, 0)

	for rows.Next() {
		var b models.Budget
		if err := rows.Scan(&b.ID, &b.UserID, &b.Name, &b.Icon, &b.MonthlyIncome,
			&b.Currency, &b.BillingPeriodMonths, &b.BillingCutoffDay, &b.Mode,
			&b.CreatedAt, &b.UpdatedAt); err != nil {
			continue
		}
		budgets = append(budgets, linkableBudget{Budget: b, Sections: make([]sectionWithCategories, 0)})
		budgetIDs = append(budgetIDs, b.ID)
	}

	if len(budgets) == 0 {
		return c.JSON([]struct{}{})
	}

	// Build budget index map.
	budgetIdx := make(map[uuid.UUID]int, len(budgets))
	for i, b := range budgets {
		budgetIdx[b.ID] = i
	}

	// Fetch sections for all budgets.
	sectionRows, err := database.DB.Pool.Query(ctx, `
		SELECT id, budget_id, name, allocation_value, icon, sort_order, created_at
		FROM budget_categories
		WHERE budget_id = ANY($1)
		ORDER BY sort_order, created_at
	`, budgetIDs)
	if err != nil {
		return errInternal(c, "failed to fetch sections")
	}
	defer sectionRows.Close()

	sectionIDs := make([]uuid.UUID, 0)
	sectionIdx := make(map[uuid.UUID]struct{ budgetIdx, sectionIdx int })

	for sectionRows.Next() {
		var s models.Section
		if err := sectionRows.Scan(&s.ID, &s.BudgetID, &s.Name, &s.AllocationValue,
			&s.Icon, &s.SortOrder, &s.CreatedAt); err != nil {
			continue
		}
		bi, ok := budgetIdx[s.BudgetID]
		if !ok {
			continue
		}
		si := len(budgets[bi].Sections)
		budgets[bi].Sections = append(budgets[bi].Sections, sectionWithCategories{
			Section:    s,
			Categories: make([]models.Category, 0),
		})
		sectionIdx[s.ID] = struct{ budgetIdx, sectionIdx int }{bi, si}
		sectionIDs = append(sectionIDs, s.ID)
	}

	// Fetch categories for all sections.
	if len(sectionIDs) > 0 {
		catRows, err := database.DB.Pool.Query(ctx, `
			SELECT id, category_id, name, allocation_value, icon, sort_order, created_at
			FROM budget_subcategories
			WHERE category_id = ANY($1)
			ORDER BY sort_order, created_at
		`, sectionIDs)
		if err == nil {
			defer catRows.Close()
			for catRows.Next() {
				var cat models.Category
				if err := catRows.Scan(&cat.ID, &cat.CategoryID, &cat.Name,
					&cat.AllocationValue, &cat.Icon, &cat.SortOrder, &cat.CreatedAt); err != nil {
					continue
				}
				idx, ok := sectionIdx[cat.CategoryID]
				if !ok {
					continue
				}
				budgets[idx.budgetIdx].Sections[idx.sectionIdx].Categories = append(
					budgets[idx.budgetIdx].Sections[idx.sectionIdx].Categories, cat)
			}
		}
	}

	return c.JSON(budgets)
}

// sectionWithCategories is used by GetLinkableBudgets to return sections with their categories.
type sectionWithCategories struct {
	models.Section
	Categories []models.Category `json:"categories"`
}

// fetchTargetBudgetIDs returns all target budget IDs that have links from the given source budget.
// Used for cross-budget WS broadcasting.
func fetchTargetBudgetIDs(sourceBudgetID uuid.UUID) ([]string, error) {
	if database.DB == nil || database.DB.Pool == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	rows, err := database.DB.Pool.Query(ctx,
		"SELECT DISTINCT target_budget_id::text FROM budget_links WHERE source_budget_id = $1",
		sourceBudgetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
