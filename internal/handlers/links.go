package handlers

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"github.com/the-financial-workspace/backend/internal/ws"
)

const maxLinksPerBudget = 10

// linkTargetsCache memoises fetchTargetBudgetIDs results for a short window.
// broadcastToLinkedTargets is called on every expense / category mutation,
// and the vast majority of budgets have no links — without this cache every
// mutation fires a DB round-trip that returns nothing. Cache hits AND misses
// are recorded; writes to budget_links invalidate the affected source budget
// (invalidateLinkTargetsCache).
var (
	linkTargetsCacheTTL = 30 * time.Second
	linkTargetsCacheMu  sync.RWMutex
	linkTargetsCache    = map[uuid.UUID]linkTargetsCacheEntry{}
)

type linkTargetsCacheEntry struct {
	ids       []uuid.UUID
	expiresAt time.Time
}

// invalidateLinkTargetsCache drops cached target IDs for the given source
// budget. Called from CreateLink, DeleteLink, and anywhere budget_links rows
// are inserted / deleted for a specific source budget.
func invalidateLinkTargetsCache(sourceBudgetIDs ...uuid.UUID) {
	if len(sourceBudgetIDs) == 0 {
		return
	}
	linkTargetsCacheMu.Lock()
	for _, id := range sourceBudgetIDs {
		delete(linkTargetsCache, id)
	}
	linkTargetsCacheMu.Unlock()
}

// invalidateLinkTargetsCacheAll drops every entry. Used after bulk deletes
// (e.g. removing a collaborator clears their links across many budgets) where
// tracking exact source IDs is not worth the complexity.
func invalidateLinkTargetsCacheAll() {
	linkTargetsCacheMu.Lock()
	linkTargetsCache = map[uuid.UUID]linkTargetsCacheEntry{}
	linkTargetsCacheMu.Unlock()
}

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
		SELECT id, source_budget_id, target_budget_id, source_category_id,
		       filter_mode, created_by, created_at
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
			&l.SourceCategoryID, &l.FilterMode, &l.CreatedBy, &l.CreatedAt); err != nil {
			return errInternal(c, "failed to parse link")
		}
		links = append(links, l)
	}

	return c.JSON(links)
}

// CreateLink creates a new budget link from a source category into the
// target budget. Section-level links no longer exist; every link targets a
// single source category.
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
		SourceBudgetID   uuid.UUID `json:"source_budget_id"`
		SourceCategoryID uuid.UUID `json:"source_category_id"`
		FilterMode       string    `json:"filter_mode"`
	}
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	// Validate filter mode.
	if req.FilterMode != "all" && req.FilterMode != "mine" {
		return errBadRequest(c, "filter_mode must be 'all' or 'mine'")
	}

	// Required-field checks happen before any DB work so callers get crisp
	// 400s instead of a downstream 404 from a missing UUID being treated as
	// a lookup miss.
	if req.SourceBudgetID == uuid.Nil {
		return errBadRequest(c, "source_budget_id is required")
	}
	if req.SourceCategoryID == uuid.Nil {
		return errBadRequest(c, "source_category_id is required")
	}

	// No self-linking.
	if req.SourceBudgetID == budgetID {
		return errBadRequest(c, "cannot link a budget to itself")
	}

	// User must have access to both budgets. These two verifications are
	// kept as separate calls so the error message distinguishes "target
	// not found" vs "source not found" — fusing them would lose that.
	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "target budget not found")
	}
	if err := verifyBudgetAccess(req.SourceBudgetID, userID); err != nil {
		return errNotFound(c, "source budget not found")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// PERF: one round-trip now covers all four of the downstream invariants
	// (currencies match, source category exists, link-count below cap, no
	// reverse link). Previously this was four separate SELECTs issued in
	// sequence; the combined form still returns per-check booleans/values
	// so the handler can emit the precise 400/404 for each failure.
	var (
		targetCurrency, sourceCurrency string
		categoryExists, reverseExists  bool
		linkCount                      int
	)
	err := database.DB.Pool.QueryRow(ctx, `
		SELECT
			(SELECT currency FROM budgets WHERE id = $1),
			(SELECT currency FROM budgets WHERE id = $2),
			EXISTS(SELECT 1 FROM budget_categories WHERE id = $3 AND budget_id = $2),
			(SELECT COUNT(*) FROM budget_links WHERE target_budget_id = $1),
			EXISTS(SELECT 1 FROM budget_links
			         WHERE source_budget_id = $1 AND target_budget_id = $2)
	`, budgetID, req.SourceBudgetID, req.SourceCategoryID).Scan(
		&targetCurrency, &sourceCurrency, &categoryExists, &linkCount, &reverseExists,
	)
	if err != nil {
		log.Printf("[links] preflight failed: %v", err)
		return errInternal(c, "failed to verify link preconditions")
	}
	if targetCurrency != sourceCurrency {
		return errBadRequest(c, "budgets must have the same currency")
	}
	if !categoryExists {
		return errNotFound(c, "source category not found in source budget")
	}
	if linkCount >= maxLinksPerBudget {
		return errBadRequest(c, "maximum number of links reached")
	}
	if reverseExists {
		return errBadRequest(c, "cannot create circular link between budgets")
	}

	// Insert the link. The `(target_budget_id, source_category_id)` UNIQUE
	// constraint surfaces as a pgconn.PgError with code 23505; translate
	// that to a clear 400 instead of a generic 500.
	var link models.BudgetLink
	err = database.DB.Pool.QueryRow(ctx, `
		INSERT INTO budget_links (source_budget_id, target_budget_id, source_category_id, filter_mode, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, source_budget_id, target_budget_id, source_category_id, filter_mode, created_by, created_at
	`, req.SourceBudgetID, budgetID, req.SourceCategoryID, req.FilterMode, userID,
	).Scan(&link.ID, &link.SourceBudgetID, &link.TargetBudgetID,
		&link.SourceCategoryID, &link.FilterMode, &link.CreatedBy, &link.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return errBadRequest(c, "this category is already linked to the target budget")
		}
		log.Printf("[links] insert failed: %v", err)
		return errInternal(c, "failed to create link")
	}

	invalidateLinkTargetsCache(req.SourceBudgetID)

	// PERF: new link changes the linked_categories view on the target budget's
	// summary.
	invalidateBudget(budgetID)

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

	// PERF: fuse access check into the UPDATE via EXISTS on budgets. The
	// previous version fired verifyBudgetAccess first (one extra round-trip);
	// now a single UPDATE...RETURNING either matches all three invariants
	// (link id, budget id, access) or returns no rows (404).
	var link models.BudgetLink
	err := database.DB.Pool.QueryRow(ctx, `
		UPDATE budget_links SET filter_mode = $1
		WHERE id = $2 AND target_budget_id = $3
		  AND EXISTS (
		    SELECT 1 FROM budgets WHERE id = $3 AND user_id = $4
		    UNION ALL
		    SELECT 1 FROM budget_collaborators WHERE budget_id = $3 AND user_id = $4
		  )
		RETURNING id, source_budget_id, target_budget_id, source_category_id, filter_mode, created_by, created_at
	`, req.FilterMode, linkID, budgetID, userID,
	).Scan(&link.ID, &link.SourceBudgetID, &link.TargetBudgetID,
		&link.SourceCategoryID, &link.FilterMode, &link.CreatedBy, &link.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "link not found")
		}
		log.Printf("[links] update failed id=%s budget=%s: %v", linkID, budgetID, err)
		return errInternal(c, "failed to update link")
	}

	invalidateBudget(budgetID)

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// PERF: fuse the access check into the DELETE...RETURNING. Previously
	// this fired verifyBudgetAccess first and then a separate DELETE — two
	// round-trips. The combined form gates on access via EXISTS; no rows
	// means "link not found OR no access" which is always safe to report
	// as 404 (don't leak link existence to non-members).
	var sourceBudgetID uuid.UUID
	err := database.DB.Pool.QueryRow(ctx, `
		DELETE FROM budget_links
		WHERE id = $1 AND target_budget_id = $2
		  AND EXISTS (
		    SELECT 1 FROM budgets WHERE id = $2 AND user_id = $3
		    UNION ALL
		    SELECT 1 FROM budget_collaborators WHERE budget_id = $2 AND user_id = $3
		  )
		RETURNING source_budget_id
	`, linkID, budgetID, userID).Scan(&sourceBudgetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "link not found")
		}
		log.Printf("[links] delete failed id=%s budget=%s: %v", linkID, budgetID, err)
		return errInternal(c, "failed to delete link")
	}

	invalidateLinkTargetsCache(sourceBudgetID)
	invalidateBudget(budgetID)

	broadcast(budgetID.String(), ws.MessageTypeLinkDeleted, fiber.Map{"id": linkID})
	broadcast(sourceBudgetID.String(), ws.MessageTypeLinkDeleted, fiber.Map{"id": linkID})

	return c.SendStatus(fiber.StatusNoContent)
}

// GetLinkableBudgets returns budgets the user has access to (excluding the
// current one) that share the same currency, together with their flat
// category lists.
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

	// PERF: fetch linkable budgets in a single round-trip by inlining the
	// source budget's currency as a scalar subquery. Previously we ran two
	// queries (SELECT currency, then SELECT matching budgets); now it's one.
	// FIX: monthly_income is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	rows, err := database.DB.Pool.Query(ctx, `
		SELECT id, user_id, name, icon, monthly_income::float8, currency,
		       billing_period_months, billing_cutoff_day, mode, created_at, updated_at
		FROM budgets
		WHERE currency = (SELECT currency FROM budgets WHERE id = $1)
		  AND id != $1
		  AND (user_id = $2 OR id IN (
		    SELECT budget_id FROM budget_collaborators WHERE user_id = $2
		  ))
		ORDER BY name
	`, budgetID, userID)
	if err != nil {
		return errInternal(c, "failed to fetch linkable budgets")
	}
	defer rows.Close()

	type linkableBudget struct {
		models.Budget
		Categories []models.Category `json:"categories"`
	}

	// Pre-allocated as a zero-length slice so an empty result marshals as
	// `[]` (not `null`) without needing a special-case early return.
	budgets := make([]linkableBudget, 0)
	budgetIDs := make([]uuid.UUID, 0)

	for rows.Next() {
		var b models.Budget
		if err := rows.Scan(&b.ID, &b.UserID, &b.Name, &b.Icon, &b.MonthlyIncome,
			&b.Currency, &b.BillingPeriodMonths, &b.BillingCutoffDay, &b.Mode,
			&b.CreatedAt, &b.UpdatedAt); err != nil {
			continue
		}
		budgets = append(budgets, linkableBudget{Budget: b, Categories: make([]models.Category, 0)})
		budgetIDs = append(budgetIDs, b.ID)
	}

	if len(budgets) == 0 {
		return c.JSON(budgets)
	}

	// Build budget index map.
	budgetIdx := make(map[uuid.UUID]int, len(budgets))
	for i, b := range budgets {
		budgetIdx[b.ID] = i
	}

	// Fetch categories for all budgets in one batched query.
	// FIX: allocation_value is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	catRows, err := database.DB.Pool.Query(ctx, `
		SELECT id, budget_id, name, allocation_value::float8, icon, sort_order, created_at
		FROM budget_categories
		WHERE budget_id = ANY($1)
		ORDER BY sort_order, created_at
	`, budgetIDs)
	if err != nil {
		return errInternal(c, "failed to fetch categories")
	}
	defer catRows.Close()

	for catRows.Next() {
		var cat models.Category
		if err := catRows.Scan(&cat.ID, &cat.BudgetID, &cat.Name, &cat.AllocationValue,
			&cat.Icon, &cat.SortOrder, &cat.CreatedAt); err != nil {
			continue
		}
		idx, ok := budgetIdx[cat.BudgetID]
		if !ok {
			continue
		}
		budgets[idx].Categories = append(budgets[idx].Categories, cat)
	}

	return c.JSON(budgets)
}

// fetchTargetBudgetIDs returns all target budget IDs that have links from the
// given source budget. Used for cross-budget WS broadcasting and cache
// invalidation. Results are cached for linkTargetsCacheTTL to avoid hammering
// the DB from every mutation when most budgets have no links.
//
// ctx is honoured for the uncached DB round-trip so cancelled HTTP requests
// propagate through.
func fetchTargetBudgetIDs(ctx context.Context, sourceBudgetID uuid.UUID) ([]uuid.UUID, error) {
	if database.DB == nil || database.DB.Pool == nil {
		return nil, nil
	}

	now := time.Now()
	linkTargetsCacheMu.RLock()
	entry, ok := linkTargetsCache[sourceBudgetID]
	linkTargetsCacheMu.RUnlock()
	if ok && now.Before(entry.expiresAt) {
		return entry.ids, nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	rows, err := database.DB.Pool.Query(queryCtx,
		"SELECT DISTINCT target_budget_id FROM budget_links WHERE source_budget_id = $1",
		sourceBudgetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}

	linkTargetsCacheMu.Lock()
	linkTargetsCache[sourceBudgetID] = linkTargetsCacheEntry{
		ids:       ids,
		expiresAt: now.Add(linkTargetsCacheTTL),
	}
	linkTargetsCacheMu.Unlock()

	return ids, nil
}
