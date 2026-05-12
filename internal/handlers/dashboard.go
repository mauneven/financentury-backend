package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/the-financial-workspace/backend/internal/cache"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
)

// dashboardEnvelope is the wire shape of the aggregated dashboard endpoint.
//
// PERF: collapses four GETs (summary / expenses / trends / budget-resume)
// into one envelope. Each sub-section can be cached / served independently
// downstream — frontends populate their per-query caches via setQueryData
// so existing useBudgetSummary/useBudgetExpenses/etc. hit the cache without
// firing their own fetch.
//
// The "expenses" key carries the same-shaped payload as ListExpenses
// (an array of Expense). Pagination is fixed to the default page (limit 100,
// offset 0) — that's what every dashboard mount uses anyway. Callers that
// need a deeper page hit /budgets/:id/expenses directly.
type dashboardEnvelope struct {
	Summary  *models.BudgetSummary        `json:"summary"`
	Expenses []models.Expense             `json:"expenses"`
	Trends   *models.TrendsResponse       `json:"trends"`
	Resume   *models.BudgetResumeResponse `json:"resume"`
}

// GetBudgetDashboard returns a single envelope holding the four reads a
// dashboard mount currently fans out as separate HTTP requests.
//
// PERF win:
//   - 4 GETs -> 1 GET (saves 3 RTTs of head-of-line latency).
//   - 4 sets of headers and 4 JSON bodies -> 1 (better gzip ratio).
//   - 4 ETag computations -> 1 (cacheControlAndETag middleware still fires
//     once on this path).
//   - The four downstream builders run in parallel via errgroup, all on
//     the same request context. Each one still hits the existing in-process
//     cache + (now) singleflight, so a hot dashboard ends up O(1) DB calls.
func GetBudgetDashboard(c *fiber.Ctx) error {
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

	// Eager access check so we 404 early without touching the four
	// builders (each of which would also 404 separately).
	if _, err := loadBudgetWithAccessSF(reqCtx, budgetID, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound(c, "budget not found")
		}
		log.Printf("[dashboard] access check budget=%s user=%s: %v", budgetID, userID, err)
		return errInternal(c, "failed to fetch budget")
	}

	env := dashboardEnvelope{}
	g, gCtx := errgroup.WithContext(reqCtx)

	// summary
	g.Go(func() error {
		s, err := buildBudgetSummary(gCtx, budgetID, userID, userToday)
		if err != nil {
			return err
		}
		env.Summary = s
		return nil
	})

	// expenses (default-paginated list)
	g.Go(func() error {
		exps, err := buildBudgetExpenses(gCtx, budgetID, 100, 0)
		if err != nil {
			return err
		}
		env.Expenses = exps
		return nil
	})

	// trends
	g.Go(func() error {
		t, err := buildBudgetTrends(gCtx, budgetID, userID, userToday)
		if err != nil {
			return err
		}
		env.Trends = t
		return nil
	})

	// resume
	g.Go(func() error {
		r, err := buildBudgetResume(gCtx, budgetID, userID, userToday)
		if err != nil {
			return err
		}
		env.Resume = r
		return nil
	})

	if err := g.Wait(); err != nil {
		log.Printf("[dashboard] partial fetch budget=%s user=%s: %v", budgetID, userID, err)
		return errInternal(c, "failed to build dashboard")
	}

	// Direct json.Marshal + Send so we skip the fiber JSON helper's tiny
	// allocation overhead. The cacheControlAndETag middleware still fires
	// after the handler — its work runs once on this big body instead of
	// four times on small ones.
	payload, err := json.Marshal(env)
	if err != nil {
		return errInternal(c, "failed to encode dashboard")
	}
	c.Set("Content-Type", "application/json")
	return c.Send(payload)
}

// buildBudgetSummary is the shared builder used by both /summary and
// /dashboard. It mirrors GetBudgetSummary's body without writing to a Fiber
// context — output is the wire-ready *BudgetSummary plus an error.
//
// The function pulls from the existing in-process cache when available and
// stores the JSON-marshalled bytes there on miss so subsequent /summary
// hits within the TTL window reuse the work.
func buildBudgetSummary(
	ctx context.Context,
	budgetID, userID uuid.UUID,
	userToday time.Time,
) (*models.BudgetSummary, error) {
	cacheKey := cache.Key{
		BudgetID: budgetID,
		UserID:   userID,
		Tag:      "summary|" + userToday.Format("2006-01-02"),
	}
	if cached, ok := cache.Get(cacheKey); ok {
		var resp models.BudgetSummary
		if err := json.Unmarshal(cached, &resp); err == nil {
			return &resp, nil
		}
		// Corrupted cache entry — fall through and rebuild. Safer than
		// returning an error to the caller.
	}

	budget, err := loadBudgetWithAccessSF(ctx, budgetID, userID)
	if err != nil {
		return nil, err
	}

	var periodStart time.Time
	if budget.BillingPeriodMonths > 0 {
		periodStart = ComputeBillingPeriodStart(userToday, budget.BillingCutoffDay, budget.BillingPeriodMonths)
	}

	categories, err := loadBudgetCategoriesSF(ctx, budgetID)
	if err != nil {
		return nil, err
	}

	ownAggs, err := loadOwnExpenseAggregatesSF(ctx, budgetID, periodStart, budget.BillingPeriodMonths == 0)
	if err != nil {
		return nil, err
	}

	linkedAggs, err := loadLinkedAggregatesSF(ctx, budgetID, userID, userToday)
	if err != nil {
		// Best-effort: degrade to own-budget summary rather than 500. Same
		// policy as GetBudgetSummary.
		log.Printf("[dashboard] loadLinkedAggregates: %v", err)
		linkedAggs = nil
	}

	agg := aggregateSummarySpending(ownAggs, linkedAggs)

	totalSpent := roundAmount(agg.totalSpent)
	totalBudget := roundAmount(budget.MonthlyIncome)

	profileMap := map[uuid.UUID]*models.Profile{}
	if len(agg.allUserIDs) > 1 {
		if pm, profErr := loadProfilesSF(ctx, agg.allUserIDs); profErr == nil {
			profileMap = pm
		}
	}

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

	resp := &models.BudgetSummary{
		Budget:           *budget,
		Categories:       categorySummaries,
		LinkedCategories: linkedSummaries,
		TotalBudget:      totalBudget,
		TotalSpent:       totalSpent,
		SpendingByUser:   buildUserSpending(agg.budgetByUser),
	}

	// Stash bytes for future /summary GETs that miss this cache slot.
	if payload, mErr := json.Marshal(resp); mErr == nil {
		cache.Set(cacheKey, payload, summaryCacheTTL)
	}

	return resp, nil
}

// buildBudgetExpenses fetches the default-paginated expense list. The
// retention cutoff matches ListExpenses to keep the wire shape identical.
func buildBudgetExpenses(
	ctx context.Context,
	budgetID uuid.UUID,
	limit, offset int,
) ([]models.Expense, error) {
	rows, err := database.DB.Pool.Query(ctx, `
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
		return nil, err
	}
	defer rows.Close()

	expenses := make([]models.Expense, 0)
	for rows.Next() {
		var e models.Expense
		if scanErr := rows.Scan(&e.ID, &e.BudgetID, &e.CategoryID, &e.Amount,
			&e.Description, &e.ExpenseDate, &e.CreatedBy,
			&e.CreatedAt, &e.UpdatedAt); scanErr != nil {
			return nil, scanErr
		}
		expenses = append(expenses, e)
	}
	return expenses, rows.Err()
}

// buildBudgetTrends mirrors GetBudgetTrends's body without writing to a
// Fiber context. Falls back to the in-process trends cache so the next
// /trends GET within the TTL serves from memory.
func buildBudgetTrends(
	ctx context.Context,
	budgetID, userID uuid.UUID,
	_ time.Time,
) (*models.TrendsResponse, error) {
	cacheKey := cache.Key{BudgetID: budgetID, UserID: userID, Tag: "trends"}
	if cached, ok := cache.Get(cacheKey); ok {
		var resp models.TrendsResponse
		if err := json.Unmarshal(cached, &resp); err == nil {
			return &resp, nil
		}
	}

	budget, err := loadBudgetWithAccessSF(ctx, budgetID, userID)
	if err != nil {
		return nil, err
	}

	categories, err := loadBudgetCategoriesSF(ctx, budgetID)
	if err != nil {
		return nil, err
	}

	useCutoff := budget.BillingPeriodMonths != 0
	cmRaw, sfErr, _ := trendsSFGroup.Do(trendsAggregateKey(budgetID, useCutoff), func() (any, error) {
		var qrows pgx.Rows
		var qerr error
		if useCutoff {
			cutoff := expenseRetentionCutoffTime()
			qrows, qerr = database.DB.Pool.Query(ctx, `
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
			qrows, qerr = database.DB.Pool.Query(ctx, `
				SELECT category_id,
				       to_char(date_trunc('month', expense_date), 'YYYY-MM-DD') AS month,
				       SUM(amount)::float8 AS total
				FROM budget_expenses
				WHERE budget_id = $1
				GROUP BY 1, 2
				ORDER BY 1, 2
			`, budgetID)
		}
		if qerr != nil {
			return nil, qerr
		}
		defer qrows.Close()

		out := make(map[uuid.UUID][]models.MonthlyTrend)
		for qrows.Next() {
			var catID uuid.UUID
			var month string
			var total float64
			if scanErr := qrows.Scan(&catID, &month, &total); scanErr != nil {
				return nil, scanErr
			}
			out[catID] = append(out[catID], models.MonthlyTrend{
				Month:      month,
				TotalSpent: roundAmount(total),
			})
		}
		return out, qrows.Err()
	})
	if sfErr != nil {
		return nil, sfErr
	}
	catMonths := cmRaw.(map[uuid.UUID][]models.MonthlyTrend)

	categoryTrends := make([]models.CategoryTrend, 0, len(categories))
	for _, cat := range categories {
		months := catMonths[cat.ID]
		if months == nil {
			months = []models.MonthlyTrend{}
		}
		sortMonthlyTrends(months)
		categoryTrends = append(categoryTrends, models.CategoryTrend{
			CategoryID:   cat.ID,
			CategoryName: cat.Name,
			Months:       months,
		})
	}

	resp := &models.TrendsResponse{
		BudgetID:   budgetID,
		Categories: categoryTrends,
	}
	if payload, mErr := json.Marshal(resp); mErr == nil {
		cache.Set(cacheKey, payload, trendsCacheTTL)
	}
	return resp, nil
}

// buildBudgetResume mirrors GetBudgetResume's body without writing to a
// Fiber context. Honours the same in-process resume cache.
func buildBudgetResume(
	ctx context.Context,
	budgetID, userID uuid.UUID,
	userToday time.Time,
) (*models.BudgetResumeResponse, error) {
	cacheKey := cache.Key{
		BudgetID: budgetID,
		UserID:   userID,
		Tag:      "resume|" + userToday.Format("2006-01-02"),
	}
	if cached, ok := cache.Get(cacheKey); ok {
		var resp models.BudgetResumeResponse
		if err := json.Unmarshal(cached, &resp); err == nil {
			return &resp, nil
		}
	}

	budget, err := loadBudgetWithAccessSF(ctx, budgetID, userID)
	if err != nil {
		return nil, err
	}

	if budget.BillingPeriodMonths == 0 {
		type oneTimeResult struct {
			totalSpent float64
			expCount   int
		}
		raw, sfErr, _ := resumeSFGroup.Do(resumeAggregateKey(budgetID, userToday), func() (any, error) {
			var ts float64
			var ec int
			qerr := database.DB.Pool.QueryRow(ctx, `
				SELECT COALESCE(SUM(amount)::float8, 0),
				       COUNT(*)::int
				FROM budget_expenses WHERE budget_id = $1
			`, budgetID).Scan(&ts, &ec)
			if qerr != nil {
				return nil, qerr
			}
			return oneTimeResult{totalSpent: ts, expCount: ec}, nil
		})
		if sfErr != nil {
			return nil, sfErr
		}
		ot := raw.(oneTimeResult)

		var periods []models.BudgetResumePeriod
		if ot.expCount > 0 {
			periods = []models.BudgetResumePeriod{{
				PeriodStart: budget.CreatedAt.Format("2006-01-02"),
				PeriodEnd:   userToday.Format("2006-01-02"),
				Income:      roundAmount(budget.MonthlyIncome),
				TotalSpent:  roundAmount(ot.totalSpent),
				Balance:     roundAmount(budget.MonthlyIncome - ot.totalSpent),
			}}
		} else {
			periods = []models.BudgetResumePeriod{}
		}
		resp := &models.BudgetResumeResponse{
			BudgetID: budgetID,
			OneTime:  true,
			Periods:  periods,
		}
		if payload, mErr := json.Marshal(resp); mErr == nil {
			cache.Set(cacheKey, payload, resumeCacheTTL)
		}
		return resp, nil
	}

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
		resp := &models.BudgetResumeResponse{
			BudgetID: budgetID,
			Periods:  []models.BudgetResumePeriod{},
		}
		if payload, mErr := json.Marshal(resp); mErr == nil {
			cache.Set(cacheKey, payload, resumeCacheTTL)
		}
		return resp, nil
	}

	starts := make([]time.Time, len(periods))
	ends := make([]time.Time, len(periods))
	for i, p := range periods {
		starts[i] = p.start
		ends[i] = p.end
	}

	type periodAgg struct {
		idx   int
		spent float64
		cnt   int
	}
	rawAggs, sfErr, _ := resumeSFGroup.Do(resumeAggregateKey(budgetID, userToday)+":r", func() (any, error) {
		rs, qerr := database.DB.Pool.Query(ctx, `
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
		if qerr != nil {
			return nil, qerr
		}
		defer rs.Close()
		out := make([]periodAgg, 0, len(periods))
		for rs.Next() {
			var pa periodAgg
			if scanErr := rs.Scan(&pa.idx, &pa.spent, &pa.cnt); scanErr != nil {
				return nil, scanErr
			}
			out = append(out, pa)
		}
		return out, rs.Err()
	})
	if sfErr != nil {
		return nil, sfErr
	}
	aggs := rawAggs.([]periodAgg)
	result := make([]models.BudgetResumePeriod, 0, len(periods))
	for _, pa := range aggs {
		idx := pa.idx
		if idx < 1 || idx > len(periods) {
			continue
		}
		p := periods[idx-1]
		endDisplay := p.end.AddDate(0, 0, -1)
		result = append(result, models.BudgetResumePeriod{
			PeriodStart: p.start.Format("2006-01-02"),
			PeriodEnd:   endDisplay.Format("2006-01-02"),
			Income:      roundAmount(income),
			TotalSpent:  roundAmount(pa.spent),
			Balance:     roundAmount(income - pa.spent),
		})
	}

	resp := &models.BudgetResumeResponse{
		BudgetID: budgetID,
		Periods:  result,
	}
	if payload, mErr := json.Marshal(resp); mErr == nil {
		cache.Set(cacheKey, payload, resumeCacheTTL)
	}
	return resp, nil
}
