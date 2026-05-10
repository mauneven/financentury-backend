package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/the-financial-workspace/backend/internal/cache"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
)

// summaryCacheTTL is how long a computed budget summary lives in the in-process
// cache before it is recomputed. The dashboard polls this endpoint on every
// mount and every websocket refresh, so even a modest TTL collapses dozens of
// DB round-trips into one on busy accounts. Writes invalidate immediately via
// cache.InvalidateBudget (see handlers in expenses.go, categories.go, links.go).
const summaryCacheTTL = 30 * time.Second

// trendsCacheTTL is the TTL for the /trends response. Trends data updates on
// the same mutation events as summary so they share an invalidation strategy.
const trendsCacheTTL = 30 * time.Second

// resumeCacheTTL is the TTL for the /budget-resume response.
const resumeCacheTTL = 30 * time.Second

// ---------- Billing-period helpers (exported for testing) ----------

// ComputeBillingPeriodStart calculates the start date of the current billing
// period given the budget's cutoff day and period length in months.
//
// Algorithm:
//  1. Clamp the cutoff day to the number of days in the current month.
//  2. If today >= clamped cutoff day, the period started this month; else
//     step back one month and clamp again.
//  3. For multi-month billing periods, shift the start back by
//     (billingPeriodMonths - 1) additional months, clamping the day again.
//
// INVARIANT (9): short-month clamp is asymmetric by design. With
// cutoffDay=31 and today=Feb 15 (2026, non-leap), `daysInMonth`=28 means
// the "current-month cutoff" clamps to 28; since day(15) < 28 we step
// back to January and clamp to 31. Result: Jan 31. The following month
// (today=Feb 28) satisfies day >= 28 and returns Feb 28 — so the period
// that began on Jan 31 lasts only 28 days, while the period beginning on
// Feb 28 lasts 31 days (through Mar 30 inclusive, since Mar 31 >= 31
// opens a fresh period). This is NOT a 1-day skip — every calendar day
// maps to exactly one period — but consumers that display "your billing
// cycle" should be aware periods straddling February are shorter when
// cutoffDay > 28. The SQL mirror in loadLinkedAggregates.periods matches
// this behaviour character for character.
func ComputeBillingPeriodStart(today time.Time, cutoffDay, billingPeriodMonths int) time.Time {
	if billingPeriodMonths < 1 {
		billingPeriodMonths = 1
	}
	if cutoffDay < 1 {
		cutoffDay = 1
	}

	year, month, day := today.Year(), today.Month(), today.Day()

	daysInMonth := daysIn(year, month)
	clampedDay := minInt(cutoffDay, daysInMonth)

	var periodStart time.Time
	if day >= clampedDay {
		periodStart = time.Date(year, month, clampedDay, 0, 0, 0, 0, time.UTC)
	} else {
		prevMonth := month - 1
		prevYear := year
		if prevMonth < 1 {
			prevMonth = 12
			prevYear--
		}
		daysInPrev := daysIn(prevYear, prevMonth)
		clampedPrev := minInt(cutoffDay, daysInPrev)
		periodStart = time.Date(prevYear, prevMonth, clampedPrev, 0, 0, 0, 0, time.UTC)
	}

	if billingPeriodMonths > 1 {
		periodStart = shiftMonths(periodStart, -(billingPeriodMonths - 1), cutoffDay)
	}

	return periodStart
}

// daysIn returns the number of days in the given month/year.
func daysIn(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// shiftMonths moves a date by n months, re-clamping the day to cutoffDay or
// the last day of the target month, whichever is smaller.
func shiftMonths(d time.Time, n int, cutoffDay int) time.Time {
	y, m, _ := d.Date()
	targetMonth := int(m) + n
	targetYear := y

	targetMonth--
	if targetMonth < 0 {
		yearsBack := (-targetMonth + 11) / 12
		targetYear -= yearsBack
		targetMonth += yearsBack * 12
	}
	targetYear += targetMonth / 12
	targetMonth = targetMonth%12 + 1

	daysTarget := daysIn(targetYear, time.Month(targetMonth))
	day := minInt(cutoffDay, daysTarget)
	return time.Date(targetYear, time.Month(targetMonth), day, 0, 0, 0, 0, time.UTC)
}

// roundAmount rounds a float64 to 2 decimal places.
func roundAmount(v float64) float64 {
	return math.Round(v*100) / 100
}

// minInt returns the smaller of a and b.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// resolveUserToday returns the current date in the user's timezone.
func resolveUserToday(c *fiber.Ctx) time.Time {
	tz := c.Get("X-Timezone")
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return time.Now().In(loc)
		}
	}
	return time.Now().UTC()
}

// expenseRetentionCutoffTime is the timestamp 12 months ago (UTC), used to
// bound expense queries for recurring budgets.
func expenseRetentionCutoffTime() time.Time {
	return time.Now().UTC().AddDate(-1, 0, 0)
}

// sortUserSpendingDesc sorts a UserSpending slice descending by Amount using
// insertion sort (typical N is 2-10 — a handful of collaborators per budget).
func sortUserSpendingDesc(us []models.UserSpending) {
	for i := 1; i < len(us); i++ {
		key := us[i]
		j := i - 1
		for j >= 0 && us[j].Amount < key.Amount {
			us[j+1] = us[j]
			j--
		}
		us[j+1] = key
	}
}

// sortMonthlyTrends sorts a slice of MonthlyTrend by Month ascending.
func sortMonthlyTrends(trends []models.MonthlyTrend) {
	for i := 1; i < len(trends); i++ {
		key := trends[i]
		j := i - 1
		for j >= 0 && trends[j].Month > key.Month {
			trends[j+1] = trends[j]
			j--
		}
		trends[j+1] = key
	}
}

// ---------- Summary-spending aggregation (pure, unit-testable) ----------

// categoryAgg is the per-category spending bucket produced by
// aggregateSummarySpending. Fields are exported-like (lower-case, same
// package) so tests in this package can inspect them directly without a
// round-trip through JSON.
type categoryAgg struct {
	totalSpent float64
	count      int
	byUser     map[uuid.UUID]float64
}

// summaryAggregation is the fully-rolled-up view produced from own and
// linked aggregate rows. Every downstream step of GetBudgetSummary (the
// per-category output, the top-level spending_by_user rollup, the profile
// fetch) pulls from one of these three fields.
type summaryAggregation struct {
	// totalSpent is the UNROUNDED sum of every own-expense amount plus
	// every linked-aggregate amount the SQL layer accepted (see invariant
	// 2). Round at response-assembly time, not here, so tests can assert
	// exact floats and so accumulated rounding error doesn't creep in.
	totalSpent float64

	// categories[cat_id] aggregates own-expense rows for that category.
	// Linked source-category aggregates go to assembleLinkedSummaries, NOT
	// into this map — so `categories[cat_id].totalSpent` is always the
	// period-filtered own-expense sum (invariant 4).
	categories map[uuid.UUID]*categoryAgg

	// budgetByUser is the per-user rollup that feeds the TOP-LEVEL
	// spending_by_user slice on BudgetSummary.
	//
	// INVARIANT (3):
	//   - own expenses: every contributor gets a row.
	//   - linked filter_mode=mine: the viewer's share rolls in under the
	//     viewer's user_id (the SQL contract guarantees CreatedBy here
	//     is the viewer).
	//   - linked filter_mode=all: NOT rolled in — those contributors are
	//     not target-budget collaborators, so their amounts land only in
	//     totalSpent (and in the per-linked-category breakdown).
	budgetByUser map[uuid.UUID]float64

	// allUserIDs is every user id that can surface in ANY spending_by_user
	// slice we emit — top-level, per-category, or per-linked-category.
	// The single profiles batch is keyed off this set. Missing an id here
	// means "profile: null" in the wire response, which is a regression
	// we specifically fix for filter_mode=all linked contributors.
	allUserIDs map[uuid.UUID]struct{}
}

// aggregateSummarySpending rolls own- and linked-expense aggregate rows
// into the shape GetBudgetSummary needs. Pure, no IO, no clock, no config
// — everything is deterministic in its inputs, which makes the hot
// correctness invariants trivially unit-testable.
//
// Preconditions (enforced by SQL, not re-checked here):
//   - ownAggs rows are already period-filtered by loadOwnExpenseAggregates
//     (invariant 4, 7).
//   - linkedAggs rows are already filter_mode-filtered by
//     loadLinkedAggregates' WHERE clause (filter_mode='all' admits every
//     contributor, filter_mode='mine' admits only the viewer).
func aggregateSummarySpending(ownAggs []ownAggRow, linkedAggs []linkedAggRow) summaryAggregation {
	agg := summaryAggregation{
		categories:   make(map[uuid.UUID]*categoryAgg, len(ownAggs)),
		budgetByUser: make(map[uuid.UUID]float64),
		allUserIDs:   make(map[uuid.UUID]struct{}),
	}

	// Own expenses: contribute to totalSpent, per-category bucket, and
	// top-level budgetByUser. A NULL created_by (legacy data) still rolls
	// into totalSpent and the category's totalSpent/count, but produces
	// no spending_by_user row — the wire shape has no "null user" slot.
	for _, row := range ownAggs {
		cat := agg.categories[row.categoryID]
		if cat == nil {
			cat = &categoryAgg{byUser: make(map[uuid.UUID]float64)}
			agg.categories[row.categoryID] = cat
		}
		cat.totalSpent += row.amount
		cat.count += row.count
		agg.totalSpent += row.amount
		if row.createdBy != nil {
			uid := *row.createdBy
			cat.byUser[uid] += row.amount
			agg.budgetByUser[uid] += row.amount
			agg.allUserIDs[uid] = struct{}{}
		}
	}

	// Linked contributions.
	//
	// INVARIANT (2): every Amount the SQL accepted counts toward totalSpent
	// regardless of filter_mode. LEFT-JOIN rows for links with zero matches
	// arrive here with Amount=0 and CreatedBy=nil, so they safely no-op.
	//
	// INVARIANT (3): only filter_mode='mine' rows roll into budgetByUser,
	// under the viewer's id. filter_mode='all' rows bypass budgetByUser.
	//
	// INVARIANT (5): track every non-nil CreatedBy in allUserIDs so the
	// profile batch covers per-linked-category spending_by_user too.
	for _, l := range linkedAggs {
		agg.totalSpent += l.Amount
		if l.CreatedBy == nil {
			continue
		}
		uid := *l.CreatedBy
		agg.allUserIDs[uid] = struct{}{}
		if l.FilterMode == "mine" {
			agg.budgetByUser[uid] += l.Amount
		}
	}

	return agg
}

// ---------- GetBudgetSummary ----------

// GetBudgetSummary computes and returns the full budget summary.
//
// OPTIMIZATION — query count reduction:
//
//	WAS (pre-optimization):
//	  1  verifyBudgetAccess
//	  2  fetchBudget
//	  3  fetchCategories
//	  4  fetchExpenses (all rows streamed back to Go)
//	  5  fetch budget_links
//	  6  fetch source budgets (ANY($1))
//	  7  fetch source categories (ANY($1))
//	  8..N+7  per-source-budget expense fetches (one query per unique source)
//	  N+8  profiles batch
//	  (+ optional second profile batch if links introduced new user IDs)
//	=> ~8 + N queries, with N = number of distinct source budgets.
//
//	NOW:
//	  1  loadBudgetWithAccess          (access check + budget fetch fused)
//	  2  loadBudgetCategories          (columns enumerated; no SELECT *)
//	  3  loadOwnExpenseAggregates      (SQL GROUP BY cat,user -> ~dozen rows)
//	  4  loadLinkedAggregates          (one query covers ALL links + source
//	                                    budgets + source categories + source
//	                                    expense aggregates; N+1 eliminated)
//	  5  loadProfiles                  (single ANY($1::uuid[]) fetch)
//	=> 5 queries, INDEPENDENT of link count.
//
//	And with the 30s in-process cache, repeat requests from the same user
//	within the window cost ZERO DB hits.
//
// The JSON response shape is unchanged.
func GetBudgetSummary(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	userToday := resolveUserToday(c)

	// --- In-process cache lookup (30s TTL) ------------------------------
	// Keyed by budget + user + today's date so per-user `filter_mode=mine`
	// aggregates never collide across collaborators and day rollovers
	// force a recomputation of the billing-period boundary.
	cacheKey := cache.Key{
		BudgetID: budgetID,
		UserID:   userID,
		Tag:      "summary|" + userToday.Format("2006-01-02"),
	}
	if cached, ok := cache.Get(cacheKey); ok {
		c.Set("Content-Type", "application/json")
		return c.Send(cached)
	}

	reqCtx := c.Context()

	// --- Query 1: access check + budget fetch in one round-trip ---------
	// WAS: verifyBudgetAccessCtx (1) + fetchBudgetCtx (1) = 2 queries.
	// NOW: single SQL returns the budget row iff the user is owner or
	// collaborator. No row = no access.
	budget, err := loadBudgetWithAccess(reqCtx, budgetID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "budget not found")
		}
		log.Printf("[summary] loadBudgetWithAccess budget=%s user=%s: %v", budgetID, userID, err)
		return errInternal(c, "failed to fetch budget")
	}

	var periodStart time.Time
	if budget.BillingPeriodMonths > 0 {
		periodStart = ComputeBillingPeriodStart(userToday, budget.BillingCutoffDay, budget.BillingPeriodMonths)
	}

	// --- Query 2: categories ------------------------------------------
	// WAS: `SELECT *` via the generic REST filter. NOW: explicit column
	// enumeration — one less column (no hidden *-expanded fields) keeps
	// the response bytes under control and gives the query planner the
	// exact set it needs.
	categories, err := loadBudgetCategories(reqCtx, budgetID)
	if err != nil {
		log.Printf("[summary] loadBudgetCategories budget=%s: %v", budgetID, err)
		return errInternal(c, "failed to fetch summary data")
	}

	// --- Query 3: own-expense aggregates grouped in SQL --------------
	// WAS: every expense row (category_id, amount, created_by) streamed to
	// Go, aggregated client-side.
	// NOW: SUM + COUNT grouped by (category_id, created_by). For a busy
	// budget with 500 expenses the wire payload drops from 500 rows to
	// ~categories * users rows (often <20).
	ownAggs, err := loadOwnExpenseAggregates(reqCtx, budgetID, periodStart, budget.BillingPeriodMonths == 0)
	if err != nil {
		log.Printf("[summary] loadOwnExpenseAggregates budget=%s: %v", budgetID, err)
		return errInternal(c, "failed to fetch summary data")
	}

	// --- Query 4: ONE-shot linked aggregate --------------------------
	// WAS: 3 + N queries (links + srcBudgets + srcCats + per-source
	// expense fetches).
	// NOW: single query returns (link meta, source budget, source cat,
	// (createdBy, amount, count)) rows for every link this target budget
	// has. Period start is computed per source budget inside the SQL so
	// a target with links into 3-month and 1-month budgets still fires
	// exactly one DB round-trip.
	linkedAggs, err := loadLinkedAggregates(reqCtx, budgetID, userID, userToday)
	if err != nil {
		log.Printf("[summary] loadLinkedAggregates budget=%s: %v", budgetID, err)
		// Best-effort: degrade to own-budget summary rather than 500.
		linkedAggs = nil
	}

	// --- Assemble per-category aggregates in memory -------------------
	agg := aggregateSummarySpending(ownAggs, linkedAggs)

	totalSpent := roundAmount(agg.totalSpent)
	totalBudget := roundAmount(budget.MonthlyIncome)

	// --- Query 5: profiles batch -------------------------------------
	// INVARIANT (11): keyed off allUserIDs so every user that can surface in
	// ANY spending_by_user slice (top-level, per-category, or per-linked-
	// category) has its profile fetched in one round-trip. Skipped entirely
	// for solo budgets where <=1 distinct user id was observed, since in
	// that case `buildUserSpending` would drop the slice anyway.
	profileMap := map[uuid.UUID]*models.Profile{}
	if len(agg.allUserIDs) > 1 {
		profileMap, err = loadProfiles(reqCtx, agg.allUserIDs)
		if err != nil {
			log.Printf("[summary] loadProfiles budget=%s: %v", budgetID, err)
			profileMap = map[uuid.UUID]*models.Profile{}
		}
	}

	// buildUserSpending materialises a byUser map into the wire shape.
	// INVARIANT (3d): len(byUser) <= 1 returns nil — which, combined with
	// the `omitempty` JSON tag on SpendingByUser, means the field is
	// omitted from the response entirely for empty/single-spender cases.
	buildUserSpending := func(byUser map[uuid.UUID]float64) []models.UserSpending {
		if len(byUser) <= 1 {
			return nil
		}
		result := make([]models.UserSpending, 0, len(byUser))
		for uid, amount := range byUser {
			result = append(result, models.UserSpending{
				UserID:  uid,
				Profile: profileMap[uid],
				Amount:  roundAmount(amount),
			})
		}
		sortUserSpendingDesc(result)
		return result
	}

	categorySummaries := make([]models.CategorySummary, 0, len(categories))
	for _, cat := range categories {
		catAllocated := roundAmount(cat.AllocationValue)

		var catSpent float64
		var catCount int
		var catUserSpending []models.UserSpending
		// INVARIANT (4): per-category totalSpent is the period-filtered sum
		// already computed by loadOwnExpenseAggregates — we just look up the
		// category's aggregate bucket. Linked source categories live in a
		// separate slice (linked_categories) and are not merged here.
		if catAgg, ok := agg.categories[cat.ID]; ok {
			catSpent = roundAmount(catAgg.totalSpent)
			catCount = catAgg.count
			catUserSpending = buildUserSpending(catAgg.byUser)
		}

		categorySummaries = append(categorySummaries, models.CategorySummary{
			Category:        models.SummaryCategoryView(cat),
			AllocatedAmount: catAllocated,
			TotalSpent:      catSpent,
			ExpenseCount:    catCount,
			SpendingByUser:  catUserSpending,
		})
	}

	linkedSummaries := assembleLinkedSummaries(linkedAggs, buildUserSpending)

	// INVARIANT (1): TotalBudget = budget.MonthlyIncome (rounded). Linked
	// allocations are NEVER summed in here — they appear only as separate
	// entries in the `linked_categories` slice.
	// INVARIANT (2): TotalSpent is the rounded sum of own-period expenses
	// plus every linked contribution accepted by SQL (all|mine).
	// INVARIANT (3): top-level spending_by_user comes from budgetByUser,
	// which only captures own-budget contributors plus filter_mode=mine
	// viewer rollups — never filter_mode=all source-only spenders.
	resp := models.BudgetSummary{
		Budget:           *budget,
		Categories:       categorySummaries,
		LinkedCategories: linkedSummaries,
		TotalBudget:      totalBudget,
		TotalSpent:       totalSpent,
		SpendingByUser:   buildUserSpending(agg.budgetByUser),
	}

	// Marshal once; stash in the cache; emit. On subsequent hits within
	// the TTL window the cache.Get short-circuit above returns the raw
	// bytes without re-marshaling.
	payload, err := json.Marshal(resp)
	if err != nil {
		return errInternal(c, "failed to encode summary")
	}
	cache.Set(cacheKey, payload, summaryCacheTTL)
	c.Set("Content-Type", "application/json")
	return c.Send(payload)
}

// loadBudgetWithAccess fetches the budget iff the user owns it or is a
// collaborator. Returns pgx.ErrNoRows when the user has no access.
func loadBudgetWithAccess(ctx context.Context, budgetID, userID uuid.UUID) (*models.Budget, error) {
	var b models.Budget
	// FIX: monthly_income is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	err := database.DB.Pool.QueryRow(ctx, `
		SELECT b.id, b.user_id, b.name, b.icon, b.monthly_income::float8, b.currency,
		       b.billing_period_months, b.billing_cutoff_day, b.mode,
		       b.created_at, b.updated_at
		FROM budgets b
		WHERE b.id = $1
		  AND (b.user_id = $2 OR EXISTS (
		    SELECT 1 FROM budget_collaborators c
		    WHERE c.budget_id = b.id AND c.user_id = $2
		  ))
	`, budgetID, userID).Scan(&b.ID, &b.UserID, &b.Name, &b.Icon, &b.MonthlyIncome,
		&b.Currency, &b.BillingPeriodMonths, &b.BillingCutoffDay, &b.Mode,
		&b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// loadBudgetCategories loads category columns needed for the summary response.
func loadBudgetCategories(ctx context.Context, budgetID uuid.UUID) ([]models.Category, error) {
	// FIX: allocation_value is NUMERIC — pgx binary format can't scan NUMERIC→float64
	// without ::float8 cast.
	rows, err := database.DB.Pool.Query(ctx, `
		SELECT id, budget_id, name, allocation_value::float8, icon, sort_order, created_at
		FROM budget_categories
		WHERE budget_id = $1
		ORDER BY sort_order ASC, created_at ASC
	`, budgetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.Category, 0, 8)
	for rows.Next() {
		var cat models.Category
		if err := rows.Scan(&cat.ID, &cat.BudgetID, &cat.Name, &cat.AllocationValue,
			&cat.Icon, &cat.SortOrder, &cat.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, cat)
	}
	return out, rows.Err()
}

// ownAggRow is the SQL-side grouping shape for budget-local expense
// aggregates.
type ownAggRow struct {
	categoryID uuid.UUID
	createdBy  *uuid.UUID
	amount     float64
	count      int
}

// loadOwnExpenseAggregates returns expense totals grouped by
// (category_id, created_by). For a busy budget with 500 expenses, the result
// is ~20 rows instead of 500. Massive egress reduction.
func loadOwnExpenseAggregates(ctx context.Context, budgetID uuid.UUID, periodStart time.Time, oneTime bool) ([]ownAggRow, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if oneTime {
		// One-time budget: include ALL expenses regardless of date.
		rows, err = database.DB.Pool.Query(ctx, `
			SELECT category_id, created_by,
			       SUM(amount)::float8 AS amount,
			       COUNT(*)::int AS cnt
			FROM budget_expenses
			WHERE budget_id = $1
			GROUP BY category_id, created_by
		`, budgetID)
	} else {
		rows, err = database.DB.Pool.Query(ctx, `
			SELECT category_id, created_by,
			       SUM(amount)::float8 AS amount,
			       COUNT(*)::int AS cnt
			FROM budget_expenses
			WHERE budget_id = $1
			  AND expense_date >= $2
			GROUP BY category_id, created_by
		`, budgetID, periodStart)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ownAggRow, 0, 8)
	for rows.Next() {
		var r ownAggRow
		if err := rows.Scan(&r.categoryID, &r.createdBy, &r.amount, &r.count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// linkedAggRow carries both link metadata and one (createdBy -> amount)
// aggregate row from the linked-category aggregate SQL.
type linkedAggRow struct {
	// Link fields
	LinkID           uuid.UUID
	SourceBudgetID   uuid.UUID
	TargetBudgetID   uuid.UUID
	SourceCategoryID uuid.UUID
	FilterMode       string
	LinkCreatedBy    uuid.UUID
	LinkCreatedAt    time.Time

	// Source budget fields
	SBName                string
	SBIcon                string
	SBMonthlyIncome       float64
	SBCurrency            string
	SBBillingPeriodMonths int
	SBBillingCutoffDay    int
	SBMode                string
	SBUserID              uuid.UUID
	SBCreatedAt           time.Time
	SBUpdatedAt           time.Time

	// Source category fields
	SCName            string
	SCAllocationValue float64
	SCIcon            string
	SCSortOrder       int
	SCBudgetID        uuid.UUID
	SCCreatedAt       time.Time

	// Aggregate row (nullable when link has zero matching expenses).
	CreatedBy *uuid.UUID
	Amount    float64
	Count     int
}

// loadLinkedAggregates runs ONE query that returns, for every budget_link
// whose target is the given budget, the set of linked-category expense
// aggregates grouped by created_by. Per-source-budget billing-period start
// is computed inside the SQL.
//
// filter_mode semantics:
//   - 'all': every expense in the period counts.
//   - 'mine': only expenses whose created_by equals the current viewer
//     count. The filtering happens in the SQL WHERE clause so rows filtered
//     out never leave the database.
func loadLinkedAggregates(ctx context.Context, targetBudgetID, viewerID uuid.UUID, userToday time.Time) ([]linkedAggRow, error) {
	todayStr := userToday.Format("2006-01-02")

	// CTE outline:
	//   links    -> all budget_links for this target
	//   srcb     -> each unique source budget
	//   srcc     -> each unique source category
	//   periods  -> per-source-budget billing-period start (DATE)
	//   agg      -> per-link, per-creator aggregate
	//
	// The final SELECT fans `agg` back out with link + source_budget +
	// source_category metadata attached. LEFT JOIN on agg keeps links with
	// zero matching expenses in the result so the client still sees them
	// as "no spending yet" rather than vanishing.
	const q = `
WITH today AS (
  SELECT $2::date AS d
),
links AS (
  SELECT id, source_budget_id, target_budget_id, source_category_id,
         filter_mode, created_by, created_at
  FROM budget_links
  WHERE target_budget_id = $1
),
srcb AS (
  SELECT b.*
  FROM budgets b
  WHERE b.id IN (SELECT DISTINCT source_budget_id FROM links)
),
srcc AS (
  SELECT c.*
  FROM budget_categories c
  WHERE c.id IN (SELECT DISTINCT source_category_id FROM links)
),
periods AS (
  -- Per source budget, compute the billing-period start used to filter
  -- expenses. One-time budgets (billing_period_months = 0) fall through to
  -- a sentinel far-past date so every expense row qualifies. The math
  -- mirrors ComputeBillingPeriodStart: clamp cutoff to the number of days
  -- in today's month, step back one month if today < cutoff, then shift
  -- back (months-1) more for multi-month periods.
  SELECT b.id AS source_budget_id,
         CASE
           WHEN b.billing_period_months = 0 THEN DATE '1900-01-01'
           ELSE (
             WITH base AS (
               SELECT
                 CASE
                   WHEN EXTRACT(DAY FROM (SELECT d FROM today))::int >=
                        LEAST(b.billing_cutoff_day,
                              EXTRACT(DAY FROM date_trunc('month', (SELECT d FROM today)) + interval '1 month - 1 day')::int)
                   THEN
                     date_trunc('month', (SELECT d FROM today))::date +
                     (LEAST(b.billing_cutoff_day,
                            EXTRACT(DAY FROM date_trunc('month', (SELECT d FROM today)) + interval '1 month - 1 day')::int) - 1)
                   ELSE
                     (date_trunc('month', (SELECT d FROM today)) - interval '1 month')::date +
                     (LEAST(b.billing_cutoff_day,
                            EXTRACT(DAY FROM (date_trunc('month', (SELECT d FROM today)) - interval '1 day'))::int) - 1)
                 END AS single_start
             )
             SELECT
               CASE
                 WHEN b.billing_period_months <= 1 THEN single_start
                 ELSE (
                   (date_trunc('month', single_start) -
                    make_interval(months => (b.billing_period_months - 1)))::date +
                   (LEAST(b.billing_cutoff_day,
                          EXTRACT(DAY FROM (
                            date_trunc('month', single_start) -
                            make_interval(months => (b.billing_period_months - 1)) +
                            interval '1 month - 1 day'
                          ))::int) - 1)
                 )
               END
             FROM base
           )
         END AS period_start
  FROM srcb b
),
agg AS (
  SELECT l.id           AS link_id,
         e.created_by   AS created_by,
         SUM(e.amount)::float8 AS amount,
         COUNT(*)::int  AS cnt
  FROM links l
  JOIN periods p ON p.source_budget_id = l.source_budget_id
  JOIN budget_expenses e
    ON e.budget_id   = l.source_budget_id
   AND e.category_id = l.source_category_id
   AND e.expense_date >= p.period_start
  WHERE (l.filter_mode = 'all' OR e.created_by = $3)
  GROUP BY l.id, e.created_by
)
-- FIX: b.monthly_income and c.allocation_value are NUMERIC — pgx binary format
-- can't scan NUMERIC→float64 without ::float8 casts.
SELECT l.id, l.source_budget_id, l.target_budget_id, l.source_category_id,
       l.filter_mode, l.created_by, l.created_at,
       b.name, b.icon, b.monthly_income::float8, b.currency,
       b.billing_period_months, b.billing_cutoff_day, b.mode,
       b.user_id, b.created_at, b.updated_at,
       c.name, c.allocation_value::float8, c.icon, c.sort_order, c.budget_id, c.created_at,
       agg.created_by, agg.amount, agg.cnt
FROM links l
JOIN srcb b ON b.id = l.source_budget_id
JOIN srcc c ON c.id = l.source_category_id
LEFT JOIN agg ON agg.link_id = l.id
ORDER BY l.created_at, agg.created_by
`
	rows, err := database.DB.Pool.Query(ctx, q, targetBudgetID, todayStr, viewerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]linkedAggRow, 0, 8)
	for rows.Next() {
		var r linkedAggRow
		var (
			amount *float64
			cnt    *int
		)
		if err := rows.Scan(
			&r.LinkID, &r.SourceBudgetID, &r.TargetBudgetID, &r.SourceCategoryID,
			&r.FilterMode, &r.LinkCreatedBy, &r.LinkCreatedAt,
			&r.SBName, &r.SBIcon, &r.SBMonthlyIncome, &r.SBCurrency,
			&r.SBBillingPeriodMonths, &r.SBBillingCutoffDay, &r.SBMode,
			&r.SBUserID, &r.SBCreatedAt, &r.SBUpdatedAt,
			&r.SCName, &r.SCAllocationValue, &r.SCIcon, &r.SCSortOrder, &r.SCBudgetID, &r.SCCreatedAt,
			&r.CreatedBy, &amount, &cnt,
		); err != nil {
			return nil, err
		}
		if amount != nil {
			r.Amount = *amount
		}
		if cnt != nil {
			r.Count = *cnt
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// assembleLinkedSummaries reduces the flat (link, createdBy) aggregate rows
// returned by loadLinkedAggregates into per-link LinkedCategorySummary
// objects, preserving creation order.
func assembleLinkedSummaries(
	rows []linkedAggRow,
	buildUserSpending func(map[uuid.UUID]float64) []models.UserSpending,
) []models.LinkedCategorySummary {
	if len(rows) == 0 {
		return nil
	}

	type accum struct {
		link       models.BudgetLink
		sourceBud  models.Budget
		srcCat     models.Category
		totalSpent float64
		count      int
		byUser     map[uuid.UUID]float64
	}
	idxByLink := make(map[uuid.UUID]*accum, 4)
	order := make([]uuid.UUID, 0, 4)

	for _, r := range rows {
		a, ok := idxByLink[r.LinkID]
		if !ok {
			a = &accum{
				link: models.BudgetLink{
					ID:               r.LinkID,
					SourceBudgetID:   r.SourceBudgetID,
					TargetBudgetID:   r.TargetBudgetID,
					SourceCategoryID: r.SourceCategoryID,
					FilterMode:       r.FilterMode,
					CreatedBy:        r.LinkCreatedBy,
					CreatedAt:        r.LinkCreatedAt,
				},
				sourceBud: models.Budget{
					ID:                  r.SourceBudgetID,
					UserID:              r.SBUserID,
					Name:                r.SBName,
					Icon:                r.SBIcon,
					MonthlyIncome:       r.SBMonthlyIncome,
					Currency:            r.SBCurrency,
					BillingPeriodMonths: r.SBBillingPeriodMonths,
					BillingCutoffDay:    r.SBBillingCutoffDay,
					Mode:                r.SBMode,
					CreatedAt:           r.SBCreatedAt,
					UpdatedAt:           r.SBUpdatedAt,
				},
				srcCat: models.Category{
					ID:              r.SourceCategoryID,
					BudgetID:        r.SCBudgetID,
					Name:            r.SCName,
					AllocationValue: r.SCAllocationValue,
					Icon:            r.SCIcon,
					SortOrder:       r.SCSortOrder,
					CreatedAt:       r.SCCreatedAt,
				},
				byUser: make(map[uuid.UUID]float64),
			}
			idxByLink[r.LinkID] = a
			order = append(order, r.LinkID)
		}
		a.totalSpent += r.Amount
		a.count += r.Count
		if r.CreatedBy != nil && r.Amount != 0 {
			a.byUser[*r.CreatedBy] += r.Amount
		}
	}

	out := make([]models.LinkedCategorySummary, 0, len(order))
	for _, id := range order {
		a := idxByLink[id]
		catSummary := models.CategorySummary{
			Category: models.SummaryCategoryView{
				ID:              a.srcCat.ID,
				BudgetID:        a.srcCat.BudgetID,
				Name:            a.srcCat.Name,
				AllocationValue: a.srcCat.AllocationValue,
				Icon:            a.srcCat.Icon,
				SortOrder:       a.srcCat.SortOrder,
				CreatedAt:       a.srcCat.CreatedAt,
			},
			AllocatedAmount: roundAmount(a.srcCat.AllocationValue),
			TotalSpent:      roundAmount(a.totalSpent),
			ExpenseCount:    a.count,
			SpendingByUser:  buildUserSpending(a.byUser),
		}
		out = append(out, models.LinkedCategorySummary{
			Link:         a.link,
			SourceBudget: a.sourceBud,
			Category:     catSummary,
		})
	}
	return out
}

// loadProfiles fetches profile rows for the given user IDs in a single query.
func loadProfiles(ctx context.Context, ids map[uuid.UUID]struct{}) (map[uuid.UUID]*models.Profile, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]*models.Profile{}, nil
	}
	arr := make([]uuid.UUID, 0, len(ids))
	for id := range ids {
		arr = append(arr, id)
	}
	rows, err := database.DB.Pool.Query(ctx, `
		SELECT id, email, full_name, created_at::text, updated_at::text
		FROM profiles
		WHERE id = ANY($1::uuid[])
	`, arr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[uuid.UUID]*models.Profile, len(arr))
	for rows.Next() {
		var p models.Profile
		if err := rows.Scan(&p.ID, &p.Email, &p.FullName, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		pCpy := p
		out[p.ID] = &pCpy
	}
	return out, rows.Err()
}

// ---------- GetBudgetTrends ----------

// GetBudgetTrends returns monthly spending per category.
//
// OPTIMIZATION — query count & payload:
//
//	WAS:
//	  1 verifyBudgetAccess
//	  2 fetchBudget
//	  3 fetchCategories
//	  4 fetchExpenses (every expense streamed back; Go did the bucketing)
//	=> 4 queries, payload linear in N expenses.
//
//	NOW:
//	  1 loadBudgetWithAccess        (access + budget fused)
//	  2 loadBudgetCategories
//	  3 aggregate grouped by date_trunc('month', expense_date) in SQL
//	=> 3 queries, payload linear in (categories * months).
func GetBudgetTrends(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	cacheKey := cache.Key{BudgetID: budgetID, UserID: userID, Tag: "trends"}
	if cached, ok := cache.Get(cacheKey); ok {
		c.Set("Content-Type", "application/json")
		return c.Send(cached)
	}

	reqCtx := c.Context()
	budget, err := loadBudgetWithAccess(reqCtx, budgetID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "budget not found")
		}
		log.Printf("[trends] loadBudgetWithAccess: %v", err)
		return errInternal(c, "failed to fetch budget")
	}

	categories, err := loadBudgetCategories(reqCtx, budgetID)
	if err != nil {
		return errInternal(c, "failed to fetch trends data")
	}

	// Aggregate in SQL: one row per (category_id, month_start_date). We
	// pick `to_char(date_trunc('month', expense_date), 'YYYY-MM-DD')` to
	// match the wire format of the original implementation (which passed
	// raw expense_date strings). Consumers render the month label from
	// this date, so the day component being the first-of-month is fine.
	var rows pgx.Rows
	useCutoff := budget.BillingPeriodMonths != 0
	if useCutoff {
		cutoff := expenseRetentionCutoffTime()
		rows, err = database.DB.Pool.Query(reqCtx, `
			SELECT category_id,
			       to_char(date_trunc('month', expense_date), 'YYYY-MM-DD') AS month,
			       SUM(amount)::float8 AS total
			FROM budget_expenses
			WHERE budget_id = $1
			  AND expense_date >= $2
			GROUP BY 1, 2
			ORDER BY 1, 2
		`, budgetID, cutoff)
	} else {
		rows, err = database.DB.Pool.Query(reqCtx, `
			SELECT category_id,
			       to_char(date_trunc('month', expense_date), 'YYYY-MM-DD') AS month,
			       SUM(amount)::float8 AS total
			FROM budget_expenses
			WHERE budget_id = $1
			GROUP BY 1, 2
			ORDER BY 1, 2
		`, budgetID)
	}
	if err != nil {
		return errInternal(c, "failed to fetch trends data")
	}

	catMonths := make(map[uuid.UUID][]models.MonthlyTrend, len(categories))
	for rows.Next() {
		var catID uuid.UUID
		var month string
		var total float64
		if err := rows.Scan(&catID, &month, &total); err != nil {
			rows.Close()
			return errInternal(c, "failed to parse trends row")
		}
		catMonths[catID] = append(catMonths[catID], models.MonthlyTrend{
			Month:      month,
			TotalSpent: roundAmount(total),
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return errInternal(c, "failed to read trends rows")
	}

	categoryTrends := make([]models.CategoryTrend, 0, len(categories))
	for _, cat := range categories {
		months := catMonths[cat.ID]
		if months == nil {
			months = []models.MonthlyTrend{}
		}
		// Rows are already ORDER BY month ASC from the DB, but keep the
		// in-memory sort as insurance for deterministic output.
		sortMonthlyTrends(months)
		categoryTrends = append(categoryTrends, models.CategoryTrend{
			CategoryID:   cat.ID,
			CategoryName: cat.Name,
			Months:       months,
		})
	}

	resp := models.TrendsResponse{
		BudgetID:   budgetID,
		Categories: categoryTrends,
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		return errInternal(c, "failed to encode trends")
	}
	cache.Set(cacheKey, payload, trendsCacheTTL)
	c.Set("Content-Type", "application/json")
	return c.Send(payload)
}

// ---------- GetBudgetResume ----------

// GetBudgetResume returns resume data (spent vs income) per billing period.
//
// OPTIMIZATION — query count:
//
//	WAS:
//	  1 verifyBudgetAccess
//	  2 fetchBudget
//	  3 fetchExpenses "in date range" — every row from 12 months back
//	=> 3 queries.
//
//	NOW:
//	  1 loadBudgetWithAccess        (access + budget fused)
//	  2 per-period aggregate via UNNEST(period_ranges) + LEFT JOIN expenses
//	=> 2 queries, all bucketing done in SQL.
func GetBudgetResume(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	reqCtx := c.Context()
	userToday := resolveUserToday(c)

	cacheKey := cache.Key{
		BudgetID: budgetID,
		UserID:   userID,
		Tag:      "resume|" + userToday.Format("2006-01-02"),
	}
	if cached, ok := cache.Get(cacheKey); ok {
		c.Set("Content-Type", "application/json")
		return c.Send(cached)
	}

	budget, err := loadBudgetWithAccess(reqCtx, budgetID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "budget not found")
		}
		log.Printf("[resume] loadBudgetWithAccess: %v", err)
		return errInternal(c, "failed to fetch budget")
	}

	// One-time budget: single period from creation to today.
	if budget.BillingPeriodMonths == 0 {
		var totalSpent float64
		var expCount int
		err := database.DB.Pool.QueryRow(reqCtx, `
			SELECT COALESCE(SUM(amount)::float8, 0),
			       COUNT(*)::int
			FROM budget_expenses WHERE budget_id = $1
		`, budgetID).Scan(&totalSpent, &expCount)
		if err != nil {
			return errInternal(c, "failed to fetch expenses")
		}

		// Preserve original behavior: a period is returned iff there is at
		// least one expense row (matches the old len(allExpenses) > 0 check,
		// not a non-zero amount check).
		var periods []models.BudgetResumePeriod
		if expCount > 0 {
			periods = []models.BudgetResumePeriod{{
				PeriodStart: budget.CreatedAt.Format("2006-01-02"),
				PeriodEnd:   userToday.Format("2006-01-02"),
				Income:      roundAmount(budget.MonthlyIncome),
				TotalSpent:  roundAmount(totalSpent),
				Balance:     roundAmount(budget.MonthlyIncome - totalSpent),
			}}
		} else {
			periods = []models.BudgetResumePeriod{}
		}
		resp := models.BudgetResumeResponse{
			BudgetID: budgetID,
			OneTime:  true,
			Periods:  periods,
		}
		payload, _ := json.Marshal(resp)
		cache.Set(cacheKey, payload, resumeCacheTTL)
		c.Set("Content-Type", "application/json")
		return c.Send(payload)
	}

	// Recurring budget: compute up to maxPeriods completed periods.
	periodMonths := budget.BillingPeriodMonths
	cutoffDay := budget.BillingCutoffDay
	income := budget.MonthlyIncome * float64(periodMonths)

	currentStart := ComputeBillingPeriodStart(userToday, cutoffDay, periodMonths)

	maxPeriods := 12 / periodMonths
	if maxPeriods < 1 {
		maxPeriods = 1
	}

	type periodRange struct {
		start time.Time
		end   time.Time
	}
	periods := make([]periodRange, 0, maxPeriods)

	prevStart := currentStart
	for i := 0; i < maxPeriods; i++ {
		lastStart := prevStart
		prevStart = shiftMonths(prevStart, -periodMonths, cutoffDay)
		if !prevStart.Before(lastStart) {
			break
		}
		prevEnd := shiftMonths(prevStart, periodMonths, cutoffDay)
		if prevStart.Before(budget.CreatedAt.Truncate(24 * time.Hour)) {
			break
		}
		periods = append(periods, periodRange{start: prevStart, end: prevEnd})
	}

	if len(periods) == 0 {
		resp := models.BudgetResumeResponse{
			BudgetID: budgetID,
			Periods:  []models.BudgetResumePeriod{},
		}
		payload, _ := json.Marshal(resp)
		cache.Set(cacheKey, payload, resumeCacheTTL)
		c.Set("Content-Type", "application/json")
		return c.Send(payload)
	}

	// One-shot aggregation — the entire period bucketing happens in SQL.
	// The WAS path round-tripped every expense in the 12-month window
	// through Go just to produce <=12 sums; this path returns <=12 rows.
	starts := make([]time.Time, len(periods))
	ends := make([]time.Time, len(periods))
	for i, p := range periods {
		starts[i] = p.start
		ends[i] = p.end
	}

	rows, err := database.DB.Pool.Query(reqCtx, `
		WITH ranges AS (
		  SELECT generate_subscripts($2::date[], 1) AS idx,
		         unnest($2::date[]) AS ps,
		         unnest($3::date[]) AS pe
		)
		SELECT r.idx,
		       COALESCE(SUM(e.amount)::float8, 0) AS spent,
		       COUNT(e.id)::int AS cnt
		FROM ranges r
		LEFT JOIN budget_expenses e
		  ON e.budget_id = $1
		 AND e.expense_date >= r.ps
		 AND e.expense_date < r.pe
		GROUP BY r.idx
		HAVING COUNT(e.id) > 0
		ORDER BY r.idx
	`, budgetID, starts, ends)
	if err != nil {
		return errInternal(c, "failed to aggregate periods")
	}
	defer rows.Close()

	result := make([]models.BudgetResumePeriod, 0, len(periods))
	for rows.Next() {
		var idx int
		var spent float64
		var cnt int
		if err := rows.Scan(&idx, &spent, &cnt); err != nil {
			return errInternal(c, "failed to parse period row")
		}
		if idx < 1 || idx > len(periods) {
			continue
		}
		p := periods[idx-1]
		endDisplay := p.end.AddDate(0, 0, -1)
		result = append(result, models.BudgetResumePeriod{
			PeriodStart: p.start.Format("2006-01-02"),
			PeriodEnd:   endDisplay.Format("2006-01-02"),
			Income:      roundAmount(income),
			TotalSpent:  roundAmount(spent),
			Balance:     roundAmount(income - spent),
		})
	}

	resp := models.BudgetResumeResponse{
		BudgetID: budgetID,
		Periods:  result,
	}
	payload, _ := json.Marshal(resp)
	cache.Set(cacheKey, payload, resumeCacheTTL)
	c.Set("Content-Type", "application/json")
	return c.Send(payload)
}
