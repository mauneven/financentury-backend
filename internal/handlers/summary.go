package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"golang.org/x/sync/errgroup"
)

// ---------- Billing-period helpers (exported for testing) ----------

// ComputeBillingPeriodStart calculates the start date of the current billing
// period given the budget's cutoff day and period length in months.
//
// Algorithm (ported from the the database RPC get_budget_summary):
//  1. Clamp the cutoff day to the number of days in the current month.
//  2. If today >= the clamped cutoff day in the current month, the candidate
//     start is that day in the current month; otherwise go back one month
//     and clamp again.
//  3. For multi-month billing periods, shift the start back by
//     (billingPeriodMonths - 1) additional months, clamping the day again.
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
		// Period starts in the current month.
		periodStart = time.Date(year, month, clampedDay, 0, 0, 0, 0, time.UTC)
	} else {
		// Go back one month.
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

	// Multi-month periods: go back (billingPeriodMonths - 1) more months.
	if billingPeriodMonths > 1 {
		periodStart = shiftMonths(periodStart, -(billingPeriodMonths - 1), cutoffDay)
	}

	return periodStart
}

// daysIn returns the number of days in the given month/year.
func daysIn(year int, month time.Month) int {
	// The zeroth day of the next month is the last day of this month.
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// shiftMonths moves a date by n months (negative = back), re-clamping the day
// to the target cutoff day or the last day of the resulting month, whichever is
// smaller.
func shiftMonths(d time.Time, n int, cutoffDay int) time.Time {
	y, m, _ := d.Date()
	targetMonth := int(m) + n
	targetYear := y

	// Normalise month into 1..12 range.
	targetMonth-- // make 0-based
	if targetMonth < 0 {
		yearsBack := (-targetMonth + 11) / 12
		targetYear -= yearsBack
		targetMonth += yearsBack * 12
	}
	targetYear += targetMonth / 12
	targetMonth = targetMonth%12 + 1 // back to 1-based

	daysTarget := daysIn(targetYear, time.Month(targetMonth))
	day := minInt(cutoffDay, daysTarget)
	return time.Date(targetYear, time.Month(targetMonth), day, 0, 0, 0, 0, time.UTC)
}

// roundAmount rounds a float64 to 2 decimal places using math.Round.
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
// It reads the IANA timezone name from the X-Timezone header (e.g.
// "America/Bogota"). Falls back to UTC when the header is missing or
// contains an unrecognised timezone.
func resolveUserToday(c *fiber.Ctx) time.Time {
	tz := c.Get("X-Timezone")
	if tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return time.Now().In(loc)
		}
	}
	return time.Now().UTC()
}

// ---------- GetBudgetSummary ----------

// GetBudgetSummary computes and returns the full budget summary. All math is
// done in Go; the database is used purely as storage via PostgREST.
//
// The endpoint issues two independent DB queries (categories, expenses)
// after the initial budget fetch, run concurrently via errgroup.
func GetBudgetSummary(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	// 1. Verify access and fetch budget in one step. verifyBudgetAccess
	//    already hits the budgets table, so we combine the ownership/access
	//    check with the budget fetch to eliminate a redundant round-trip.
	reqCtx := c.Context()
	if err := verifyBudgetAccessCtx(reqCtx, budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	budget, err := fetchBudgetCtx(reqCtx, budgetID)
	if err != nil {
		return errInternal(c, "failed to fetch budget")
	}
	if budget == nil {
		return errNotFound(c, "budget not found")
	}

	// 2. Fetch categories and expenses concurrently. Both only need the
	//    budgetID (and periodStart for expenses). Running them in parallel
	//    saves one full round-trip to the database.
	//
	//    For one-time budgets (billing_period_months == 0), skip billing period
	//    calculation and include ALL expenses.

	// Resolve the user's timezone from the X-Timezone header so that
	// billing-period boundaries align with the user's local calendar day.
	// Falls back to UTC when the header is missing or invalid.
	userToday := resolveUserToday(c)

	var (
		categories []models.Category
		expenses   []models.Expense
	)

	g, gctx := errgroup.WithContext(reqCtx)

	g.Go(func() error {
		var fetchErr error
		categories, fetchErr = fetchCategoriesCtx(gctx, budgetID)
		if fetchErr != nil {
			return fmt.Errorf("fetch categories: %w", fetchErr)
		}
		return nil
	})

	g.Go(func() error {
		var fetchErr error
		if budget.BillingPeriodMonths == 0 {
			// One-time budget: all expenses count toward the total,
			// without the 12-month retention cutoff.
			expenses, fetchErr = fetchAllExpensesNoRetentionCtx(gctx, budgetID)
		} else {
			periodStart := ComputeBillingPeriodStart(userToday, budget.BillingCutoffDay, budget.BillingPeriodMonths)
			expenses, fetchErr = fetchExpensesForSummaryCtx(gctx, budgetID, periodStart)
		}
		if fetchErr != nil {
			return fmt.Errorf("fetch expenses: %w", fetchErr)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return errInternal(c, "failed to fetch summary data")
	}

	// 3. Aggregate expenses by category_id AND by user at each level.
	type expenseAgg struct {
		totalSpent float64
		count      int
		byUser     map[uuid.UUID]float64
	}
	expByCategory := make(map[uuid.UUID]*expenseAgg, len(categories))
	budgetByUser := make(map[uuid.UUID]float64)
	allUserIDs := make(map[uuid.UUID]struct{})

	for _, exp := range expenses {
		agg := expByCategory[exp.CategoryID]
		if agg == nil {
			agg = &expenseAgg{byUser: make(map[uuid.UUID]float64)}
			expByCategory[exp.CategoryID] = agg
		}
		agg.totalSpent += exp.Amount
		agg.count++

		// Per-user aggregation — only when created_by is set.
		if exp.CreatedBy != nil {
			uid := *exp.CreatedBy
			agg.byUser[uid] += exp.Amount
			budgetByUser[uid] += exp.Amount
			allUserIDs[uid] = struct{}{}
		}
	}

	// 3b. Batch-fetch profiles for all users who have expenses.
	//     Only do this when there are multiple spenders (shared budget).
	profileMap := make(map[uuid.UUID]*models.Profile)
	if len(allUserIDs) > 1 {
		userIDStrs := make([]string, 0, len(allUserIDs))
		for uid := range allUserIDs {
			userIDStrs = append(userIDStrs, uid.String())
		}
		profileQuery := database.NewFilter().
			Select("id,email,full_name,created_at,updated_at").
			In("id", userIDStrs).
			Build()
		profileBody, profileStatus, profileErr := database.DB.GetCtx(reqCtx, "profiles", profileQuery)
		if profileErr == nil && profileStatus == http.StatusOK {
			var profiles []models.Profile
			if err := json.Unmarshal(profileBody, &profiles); err == nil {
				for i := range profiles {
					profileMap[profiles[i].ID] = &profiles[i]
				}
			}
		}
	}

	// Helper: build sorted UserSpending slice from a per-user map.
	buildUserSpending := func(byUser map[uuid.UUID]float64) []models.UserSpending {
		if len(byUser) <= 1 {
			return nil // skip for solo budgets
		}
		result := make([]models.UserSpending, 0, len(byUser))
		for uid, amount := range byUser {
			result = append(result, models.UserSpending{
				UserID:  uid,
				Profile: profileMap[uid],
				Amount:  roundAmount(amount),
			})
		}
		// Sort descending by amount for consistent ordering.
		for i := 1; i < len(result); i++ {
			key := result[i]
			j := i - 1
			for j >= 0 && result[j].Amount < key.Amount {
				result[j+1] = result[j]
				j--
			}
			result[j+1] = key
		}
		return result
	}

	// 4. Build the response.
	totalBudget := roundAmount(budget.MonthlyIncome)

	var totalSpent float64
	for _, exp := range expenses {
		totalSpent += exp.Amount
	}
	totalSpent = roundAmount(totalSpent)

	categorySummaries := make([]models.CategorySummary, 0, len(categories))
	for _, cat := range categories {
		catAllocated := roundAmount(cat.AllocationValue)

		var catSpent float64
		var catCount int
		var catUserSpending []models.UserSpending
		if agg, ok := expByCategory[cat.ID]; ok {
			catSpent = roundAmount(agg.totalSpent)
			catCount = agg.count
			catUserSpending = buildUserSpending(agg.byUser)
		}

		categorySummaries = append(categorySummaries, models.CategorySummary{
			Category: models.SummaryCategoryView{
				ID:              cat.ID,
				BudgetID:        cat.BudgetID,
				Name:            cat.Name,
				AllocationValue: cat.AllocationValue,
				Icon:            cat.Icon,
				SortOrder:       cat.SortOrder,
				CreatedAt:       cat.CreatedAt,
			},
			AllocatedAmount: catAllocated,
			TotalSpent:      catSpent,
			ExpenseCount:    catCount,
			SpendingByUser:  catUserSpending,
		})
	}

	// 5. Fetch and aggregate linked categories from other budgets. The helper
	//    folds per-user spending from linked categories into budgetByUser so
	//    the budget-level SpendingByUser stays consistent with totalSpent.
	//    We must also refresh profileMap to include any collaborators from
	//    source budgets who appear only through linked expenses.
	linkedSummaries := buildLinkedCategories(c.Context(), budgetID, userID, userToday, profileMap, buildUserSpending, budgetByUser)
	for _, ls := range linkedSummaries {
		totalSpent = roundAmount(totalSpent + ls.Category.TotalSpent)
	}

	// After linked categories possibly added new user IDs to budgetByUser,
	// ensure we have their profiles for display. Cheap no-op when empty.
	missingUserIDs := make([]string, 0)
	for uid := range budgetByUser {
		if _, have := profileMap[uid]; !have {
			missingUserIDs = append(missingUserIDs, uid.String())
		}
	}
	if len(missingUserIDs) > 0 {
		profileQuery := database.NewFilter().
			Select("id,email,full_name,created_at,updated_at").
			In("id", missingUserIDs).
			Build()
		profileBody, profileStatus, profileErr := database.DB.GetCtx(reqCtx, "profiles", profileQuery)
		if profileErr == nil && profileStatus == http.StatusOK {
			var profiles []models.Profile
			if err := json.Unmarshal(profileBody, &profiles); err == nil {
				for i := range profiles {
					profileMap[profiles[i].ID] = &profiles[i]
				}
			}
		}
	}

	resp := models.BudgetSummary{
		Budget:           *budget,
		Categories:       categorySummaries,
		LinkedCategories: linkedSummaries,
		TotalBudget:      totalBudget,
		TotalSpent:       totalSpent,
		SpendingByUser:   buildUserSpending(budgetByUser),
	}

	return c.JSON(resp)
}

// ---------- GetBudgetTrends ----------

// GetBudgetTrends returns daily spending data grouped by category. All
// computation is done in Go; the database is used purely as storage.
//
// Categories and expenses are fetched with maximum concurrency — they are
// independent and run in parallel.
func GetBudgetTrends(c *fiber.Ctx) error {
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

	// Fetch the budget itself first so we know whether this is a one-time
	// budget (BillingPeriodMonths == 0), which changes the expense-retention
	// semantics: for one-time budgets EVERY expense counts toward the trend,
	// regardless of age, matching the summary endpoint's behavior.
	budget, err := fetchBudgetCtx(reqCtx, budgetID)
	if err != nil || budget == nil {
		return errNotFound(c, "budget not found")
	}

	// Fetch categories and all expenses concurrently — they are independent.
	var (
		categories  []models.Category
		allExpenses []models.Expense
	)

	g, gctx := errgroup.WithContext(reqCtx)

	g.Go(func() error {
		var fetchErr error
		categories, fetchErr = fetchCategoriesCtx(gctx, budgetID)
		if fetchErr != nil {
			return fmt.Errorf("fetch categories: %w", fetchErr)
		}
		return nil
	})

	g.Go(func() error {
		var fetchErr error
		// For trends we only need category_id, amount, and expense_date.
		// One-time budgets are not bounded by the 12-month retention cutoff:
		// their lifetime IS the single billing period, so the full history
		// is required to produce meaningful monthly trends.
		if budget.BillingPeriodMonths == 0 {
			allExpenses, fetchErr = fetchExpensesForTrendsNoRetentionCtx(gctx, budgetID)
		} else {
			allExpenses, fetchErr = fetchExpensesForTrendsCtx(gctx, budgetID)
		}
		if fetchErr != nil {
			return fmt.Errorf("fetch expenses: %w", fetchErr)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return errInternal(c, "failed to fetch trends data")
	}

	// Aggregate: category -> date -> total_spent.
	categoryDailyMap := make(map[uuid.UUID]map[string]float64, len(categories))
	for _, exp := range allExpenses {
		if categoryDailyMap[exp.CategoryID] == nil {
			categoryDailyMap[exp.CategoryID] = make(map[string]float64)
		}
		// Use expense_date directly (already YYYY-MM-DD string).
		categoryDailyMap[exp.CategoryID][exp.ExpenseDate] += exp.Amount
	}

	// Build response preserving category sort order.
	categoryTrends := make([]models.CategoryTrend, 0, len(categories))
	for _, cat := range categories {
		dailyMap := categoryDailyMap[cat.ID]
		months := make([]models.MonthlyTrend, 0, len(dailyMap))
		for date, spent := range dailyMap {
			months = append(months, models.MonthlyTrend{
				Month:      date,
				TotalSpent: roundAmount(spent),
			})
		}
		// Sort months chronologically.
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

	return c.JSON(resp)
}

// sortMonthlyTrends sorts a slice of MonthlyTrend by Month (date string) in
// ascending order using a simple insertion sort (typically small N).
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

// ---------- GetBudgetResume ----------

// GetBudgetResume returns the budget resume. For recurring budgets it returns
// completed billing periods (up to 12 months back) that contain expense data.
// For one-time budgets it returns a single period from creation to now.
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
	if err := verifyBudgetAccessCtx(reqCtx, budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	budget, err := fetchBudgetCtx(reqCtx, budgetID)
	if err != nil || budget == nil {
		return errInternal(c, "failed to fetch budget")
	}

	userToday := resolveUserToday(c)

	// One-time budgets: single period from creation to now.
	if budget.BillingPeriodMonths == 0 {
		allExpenses, err := fetchAllExpensesNoRetentionCtx(reqCtx, budgetID)
		if err != nil {
			return errInternal(c, "failed to fetch expenses")
		}

		// Only return the period if there are expenses.
		var periods []models.BudgetResumePeriod
		if len(allExpenses) > 0 {
			var totalSpent float64
			for _, exp := range allExpenses {
				totalSpent += exp.Amount
			}
			totalSpent = roundAmount(totalSpent)
			periods = []models.BudgetResumePeriod{{
				PeriodStart: budget.CreatedAt.Format("2006-01-02"),
				PeriodEnd:   userToday.Format("2006-01-02"),
				Income:      roundAmount(budget.MonthlyIncome),
				TotalSpent:  totalSpent,
				Balance:     roundAmount(budget.MonthlyIncome - totalSpent),
			}}
		} else {
			periods = []models.BudgetResumePeriod{}
		}

		return c.JSON(models.BudgetResumeResponse{
			BudgetID: budgetID,
			OneTime:  true,
			Periods:  periods,
		})
	}

	// Recurring budget: completed periods going back up to 12 months.
	periodMonths := budget.BillingPeriodMonths
	cutoffDay := budget.BillingCutoffDay
	// Income reported per period scales with the number of months in the
	// billing cycle: a 3-month budget accrues 3 * monthly_income over the
	// period, not just one month's worth. Without this scaling the Balance
	// (income - spent) is wildly negative for 3/6/12-month budgets.
	income := budget.MonthlyIncome * float64(periodMonths)

	// Current period start — still in progress, skip it.
	currentStart := ComputeBillingPeriodStart(userToday, cutoffDay, periodMonths)

	maxPeriods := 12 / periodMonths
	if maxPeriods < 1 {
		maxPeriods = 1
	}

	type periodRange struct {
		start time.Time
		end   time.Time // exclusive (start of next period)
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
		return c.JSON(models.BudgetResumeResponse{
			BudgetID: budgetID,
			Periods:  []models.BudgetResumePeriod{},
		})
	}

	oldestStart := periods[len(periods)-1].start
	allExpenses, err := fetchExpensesInDateRangeCtx(reqCtx, budgetID, oldestStart, currentStart)
	if err != nil {
		return errInternal(c, "failed to fetch expenses")
	}

	type periodAgg struct {
		totalSpent float64
		hasData    bool
	}
	periodData := make([]periodAgg, len(periods))

	for _, exp := range allExpenses {
		expDate, parseErr := time.Parse("2006-01-02", exp.ExpenseDate)
		if parseErr != nil {
			continue
		}
		for i, p := range periods {
			if !expDate.Before(p.start) && expDate.Before(p.end) {
				periodData[i].totalSpent += exp.Amount
				periodData[i].hasData = true
				break
			}
		}
	}

	result := make([]models.BudgetResumePeriod, 0, len(periods))
	for i, p := range periods {
		if !periodData[i].hasData {
			continue
		}
		spent := roundAmount(periodData[i].totalSpent)
		bal := roundAmount(income - spent)
		endDisplay := p.end.AddDate(0, 0, -1)
		result = append(result, models.BudgetResumePeriod{
			PeriodStart: p.start.Format("2006-01-02"),
			PeriodEnd:   endDisplay.Format("2006-01-02"),
			Income:      roundAmount(income),
			TotalSpent:  spent,
			Balance:     bal,
		})
	}

	return c.JSON(models.BudgetResumeResponse{
		BudgetID: budgetID,
		Periods:  result,
	})
}

// ---------- Linked categories helper ----------

// buildLinkedCategories fetches and aggregates linked categories for a target
// budget. Batches all lookups (links, source budgets, categories, expenses)
// to avoid the N+1 pattern where each link fired 3–4 independent queries.
// buildLinkedCategories returns the per-link summaries AND writes raw per-user
// spending across all linked categories into budgetByUser (so the caller's
// budget-level SpendingByUser total matches TotalSpent which also includes
// linked spending). budgetByUser may be nil, in which case aggregation is
// skipped.
func buildLinkedCategories(
	ctx context.Context,
	targetBudgetID, userID uuid.UUID,
	userToday time.Time,
	profileMap map[uuid.UUID]*models.Profile,
	buildUserSpending func(map[uuid.UUID]float64) []models.UserSpending,
	budgetByUser map[uuid.UUID]float64,
) []models.LinkedCategorySummary {
	if database.DB == nil || database.DB.Pool == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Step 1: Fetch all links for this target budget.
	rows, err := database.DB.Pool.Query(ctx, `
		SELECT id, source_budget_id, target_budget_id, source_category_id,
		       filter_mode, created_by, created_at
		FROM budget_links WHERE target_budget_id = $1
	`, targetBudgetID)
	if err != nil {
		return nil
	}
	var links []models.BudgetLink
	for rows.Next() {
		var l models.BudgetLink
		if err := rows.Scan(&l.ID, &l.SourceBudgetID, &l.TargetBudgetID,
			&l.SourceCategoryID, &l.FilterMode, &l.CreatedBy, &l.CreatedAt); err != nil {
			continue
		}
		links = append(links, l)
	}
	rows.Close()
	if len(links) == 0 {
		return nil
	}

	// Step 2: Collect unique source budget IDs and source category IDs.
	srcBudgetIDSet := make(map[uuid.UUID]struct{}, len(links))
	srcCategoryIDSet := make(map[uuid.UUID]struct{}, len(links))
	for _, l := range links {
		srcBudgetIDSet[l.SourceBudgetID] = struct{}{}
		srcCategoryIDSet[l.SourceCategoryID] = struct{}{}
	}
	srcBudgetIDs := make([]uuid.UUID, 0, len(srcBudgetIDSet))
	for id := range srcBudgetIDSet {
		srcBudgetIDs = append(srcBudgetIDs, id)
	}
	srcCategoryIDs := make([]uuid.UUID, 0, len(srcCategoryIDSet))
	for id := range srcCategoryIDSet {
		srcCategoryIDs = append(srcCategoryIDs, id)
	}

	// Step 3: Batch-fetch source budgets and the referenced categories
	// concurrently.
	budgetCache := make(map[uuid.UUID]*models.Budget, len(srcBudgetIDs))
	categoryMap := make(map[uuid.UUID]*models.Category, len(srcCategoryIDs))

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		bRows, err := database.DB.Pool.Query(gctx, `
			SELECT id, user_id, name, icon, monthly_income, currency,
			       billing_period_months, billing_cutoff_day, mode, created_at, updated_at
			FROM budgets WHERE id = ANY($1)
		`, srcBudgetIDs)
		if err != nil {
			return err
		}
		defer bRows.Close()
		for bRows.Next() {
			var b models.Budget
			if err := bRows.Scan(&b.ID, &b.UserID, &b.Name, &b.Icon, &b.MonthlyIncome,
				&b.Currency, &b.BillingPeriodMonths, &b.BillingCutoffDay, &b.Mode,
				&b.CreatedAt, &b.UpdatedAt); err != nil {
				continue
			}
			budgetCpy := b
			budgetCache[b.ID] = &budgetCpy
		}
		return nil
	})

	g.Go(func() error {
		cRows, err := database.DB.Pool.Query(gctx, `
			SELECT id, budget_id, name, allocation_value, icon, sort_order, created_at
			FROM budget_categories WHERE id = ANY($1)
		`, srcCategoryIDs)
		if err != nil {
			return err
		}
		defer cRows.Close()
		for cRows.Next() {
			var cat models.Category
			if err := cRows.Scan(&cat.ID, &cat.BudgetID, &cat.Name,
				&cat.AllocationValue, &cat.Icon, &cat.SortOrder, &cat.CreatedAt); err != nil {
				continue
			}
			catCpy := cat
			categoryMap[cat.ID] = &catCpy
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return nil
	}

	// Step 4: Fetch expenses for each unique source budget — each budget has
	// its own billing period start so the date filter differs. Run all
	// expense queries concurrently.
	expensesByBudget := make(map[uuid.UUID][]models.Expense, len(srcBudgetIDs))
	var expMu sync.Mutex
	eg, egctx := errgroup.WithContext(ctx)
	for _, bid := range srcBudgetIDs {
		bid := bid
		srcBudget, ok := budgetCache[bid]
		if !ok {
			continue
		}
		eg.Go(func() error {
			var exps []models.Expense
			var err error
			if srcBudget.BillingPeriodMonths == 0 {
				exps, err = fetchAllExpensesNoRetentionCtx(egctx, bid)
			} else {
				periodStart := ComputeBillingPeriodStart(userToday, srcBudget.BillingCutoffDay, srcBudget.BillingPeriodMonths)
				exps, err = fetchExpensesForSummaryCtx(egctx, bid, periodStart)
			}
			if err != nil {
				return nil // best-effort: skip failed budgets
			}
			expMu.Lock()
			expensesByBudget[bid] = exps
			expMu.Unlock()
			return nil
		})
	}
	_ = eg.Wait()

	// Step 5: Build per-link summaries from the cached data.
	result := make([]models.LinkedCategorySummary, 0, len(links))
	for _, link := range links {
		srcBudget, ok := budgetCache[link.SourceBudgetID]
		if !ok {
			continue
		}
		cat, ok := categoryMap[link.SourceCategoryID]
		if !ok {
			continue
		}

		expenses := expensesByBudget[link.SourceBudgetID]

		// Filter expenses to this linked category + apply filter_mode.
		var catTotalSpent float64
		var catCount int
		catByUser := make(map[uuid.UUID]float64)

		for _, exp := range expenses {
			if exp.CategoryID != link.SourceCategoryID {
				continue
			}
			// Apply filter mode.
			if link.FilterMode == "mine" && (exp.CreatedBy == nil || *exp.CreatedBy != userID) {
				continue
			}

			catTotalSpent += exp.Amount
			catCount++

			if exp.CreatedBy != nil {
				uid := *exp.CreatedBy
				catByUser[uid] += exp.Amount
				// Roll raw per-user spending into the budget-level map so the
				// top-level SpendingByUser sum matches TotalSpent (which
				// already folds in each link's TotalSpent).
				if budgetByUser != nil {
					budgetByUser[uid] += exp.Amount
				}
			}
		}

		catSummary := models.CategorySummary{
			Category: models.SummaryCategoryView{
				ID:              cat.ID,
				BudgetID:        cat.BudgetID,
				Name:            cat.Name,
				AllocationValue: cat.AllocationValue,
				Icon:            cat.Icon,
				SortOrder:       cat.SortOrder,
				CreatedAt:       cat.CreatedAt,
			},
			AllocatedAmount: roundAmount(cat.AllocationValue),
			TotalSpent:      roundAmount(catTotalSpent),
			ExpenseCount:    catCount,
			SpendingByUser:  buildUserSpending(catByUser),
		}

		result = append(result, models.LinkedCategorySummary{
			Link:         link,
			SourceBudget: *srcBudget,
			Category:     catSummary,
		})
	}

	return result
}

// ---------- Data-fetching helpers ----------

// fetchBudget loads a single budget by ID. Returns nil if not found.
func fetchBudget(budgetID uuid.UUID) (*models.Budget, error) {
	return fetchBudgetCtx(context.Background(), budgetID)
}

// fetchBudgetCtx is the context-aware variant of fetchBudget.
func fetchBudgetCtx(ctx context.Context, budgetID uuid.UUID) (*models.Budget, error) {
	query := database.NewFilter().
		Select("*").
		Eq("id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.GetCtx(ctx, "budgets", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var budgets []models.Budget
	if err := json.Unmarshal(body, &budgets); err != nil {
		return nil, err
	}
	if len(budgets) == 0 {
		return nil, nil
	}
	return &budgets[0], nil
}

// fetchCategories loads all categories for a budget, ordered by sort_order.
func fetchCategories(budgetID uuid.UUID) ([]models.Category, error) {
	return fetchCategoriesCtx(context.Background(), budgetID)
}

// fetchCategoriesCtx is the context-aware variant of fetchCategories.
func fetchCategoriesCtx(ctx context.Context, budgetID uuid.UUID) ([]models.Category, error) {
	query := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("sort_order", "asc").
		Build()

	body, statusCode, err := database.DB.GetCtx(ctx, "budget_categories", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var categories []models.Category
	if err := json.Unmarshal(body, &categories); err != nil {
		return nil, err
	}
	return categories, nil
}

// fetchExpensesInPeriod loads expenses for a budget where expense_date >=
// periodStart, ordered by expense_date DESC.
func fetchExpensesInPeriod(budgetID uuid.UUID, periodStart time.Time) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Gte("expense_date", periodStart.Format("2006-01-02")).
		Order("expense_date", "desc").
		Build()

	body, statusCode, err := database.DB.Get("budget_expenses", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return nil, err
	}
	return expenses, nil
}

// fetchAllExpenses loads all expenses for a budget, ordered by expense_date DESC.
func fetchAllExpenses(budgetID uuid.UUID) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("expense_date", "desc").
		Build()

	body, statusCode, err := database.DB.Get("budget_expenses", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return nil, err
	}
	return expenses, nil
}

// fetchAllExpensesForSummary loads only the columns needed for budget summary
// aggregation (category_id, amount) for expenses within the retention window
// (12 months). Used for recurring budgets.
func fetchAllExpensesForSummary(budgetID uuid.UUID) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("category_id,amount,created_by").
		Eq("budget_id", budgetID.String()).
		Gte("expense_date", expenseRetentionCutoff()).
		Build()

	body, statusCode, err := database.DB.Get("budget_expenses", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return nil, err
	}
	return expenses, nil
}

// fetchAllExpensesNoRetention loads only the columns needed for budget summary
// aggregation (category_id, amount) for ALL expenses without applying the
// 12-month retention cutoff. Used for one-time budgets where every expense
// counts toward the total regardless of age.
func fetchAllExpensesNoRetention(budgetID uuid.UUID) ([]models.Expense, error) {
	return fetchAllExpensesNoRetentionCtx(context.Background(), budgetID)
}

// fetchAllExpensesNoRetentionCtx is the context-aware variant of
// fetchAllExpensesNoRetention.
func fetchAllExpensesNoRetentionCtx(ctx context.Context, budgetID uuid.UUID) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("category_id,amount,created_by").
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.GetCtx(ctx, "budget_expenses", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return nil, err
	}
	return expenses, nil
}

// fetchExpensesForSummary loads only the columns needed for budget summary
// aggregation (category_id, amount) within the current billing period.
// Fetching fewer columns reduces data transfer for budgets with many expenses.
func fetchExpensesForSummary(budgetID uuid.UUID, periodStart time.Time) ([]models.Expense, error) {
	return fetchExpensesForSummaryCtx(context.Background(), budgetID, periodStart)
}

// fetchExpensesForSummaryCtx is the context-aware variant of fetchExpensesForSummary.
func fetchExpensesForSummaryCtx(ctx context.Context, budgetID uuid.UUID, periodStart time.Time) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("category_id,amount,created_by").
		Eq("budget_id", budgetID.String()).
		Gte("expense_date", periodStart.Format("2006-01-02")).
		Build()

	body, statusCode, err := database.DB.GetCtx(ctx, "budget_expenses", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return nil, err
	}
	return expenses, nil
}

// fetchExpensesInDateRange loads expenses for a budget where expense_date is
// between start (inclusive) and end (exclusive).
func fetchExpensesInDateRange(budgetID uuid.UUID, start, end time.Time) ([]models.Expense, error) {
	return fetchExpensesInDateRangeCtx(context.Background(), budgetID, start, end)
}

// fetchExpensesInDateRangeCtx is the context-aware variant of fetchExpensesInDateRange.
func fetchExpensesInDateRangeCtx(ctx context.Context, budgetID uuid.UUID, start, end time.Time) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("category_id,amount,expense_date").
		Eq("budget_id", budgetID.String()).
		Gte("expense_date", start.Format("2006-01-02")).
		Lt("expense_date", end.Format("2006-01-02")).
		Build()

	body, statusCode, err := database.DB.GetCtx(ctx, "budget_expenses", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return nil, err
	}
	return expenses, nil
}

// fetchExpensesForTrends loads only the columns needed for trends aggregation
// (category_id, amount, expense_date) for all time. Ordering is unnecessary
// since we aggregate by date in Go.
func fetchExpensesForTrends(budgetID uuid.UUID) ([]models.Expense, error) {
	return fetchExpensesForTrendsCtx(context.Background(), budgetID)
}

// fetchExpensesForTrendsCtx is the context-aware variant of fetchExpensesForTrends.
func fetchExpensesForTrendsCtx(ctx context.Context, budgetID uuid.UUID) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("category_id,amount,expense_date").
		Eq("budget_id", budgetID.String()).
		Gte("expense_date", expenseRetentionCutoff()).
		Build()

	body, statusCode, err := database.DB.GetCtx(ctx, "budget_expenses", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return nil, err
	}
	return expenses, nil
}

// fetchExpensesForTrendsNoRetentionCtx loads trends-aggregation columns
// (category_id, amount, expense_date) for ALL expenses of a budget without
// applying the 12-month retention cutoff. Used for one-time budgets, whose
// lifetime may exceed 12 months and whose full expense history is
// semantically part of the single billing "period".
func fetchExpensesForTrendsNoRetentionCtx(ctx context.Context, budgetID uuid.UUID) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("category_id,amount,expense_date").
		Eq("budget_id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.GetCtx(ctx, "budget_expenses", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var expenses []models.Expense
	if err := json.Unmarshal(body, &expenses); err != nil {
		return nil, err
	}
	return expenses, nil
}
