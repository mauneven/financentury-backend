package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
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
// The endpoint issues three independent DB queries (sections, categories,
// expenses) after the initial budget fetch. Sections and expenses run
// concurrently via errgroup; categories must wait for section IDs but run
// concurrently with expense aggregation.
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
	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	budget, err := fetchBudget(budgetID)
	if err != nil {
		return errInternal(c, "failed to fetch budget")
	}
	if budget == nil {
		return errNotFound(c, "budget not found")
	}

	// 2. Fetch sections and expenses concurrently. These two queries are
	//    independent -- both only need the budgetID (and periodStart for
	//    expenses). Running them in parallel saves one full round-trip to
	//    the database.
	//
	//    For one-time budgets (billing_period_months == 0), skip billing period
	//    calculation and include ALL expenses.

	// Resolve the user's timezone from the X-Timezone header so that
	// billing-period boundaries align with the user's local calendar day.
	// Falls back to UTC when the header is missing or invalid.
	userToday := resolveUserToday(c)

	var (
		sections []models.Section
		expenses []models.Expense
	)

	g, _ := errgroup.WithContext(c.Context())

	g.Go(func() error {
		var fetchErr error
		sections, fetchErr = fetchSections(budgetID)
		if fetchErr != nil {
			return fmt.Errorf("fetch sections: %w", fetchErr)
		}
		return nil
	})

	g.Go(func() error {
		var fetchErr error
		if budget.BillingPeriodMonths == 0 {
			// One-time budget: all expenses count toward the total,
			// without the 12-month retention cutoff.
			expenses, fetchErr = fetchAllExpensesNoRetention(budgetID)
		} else {
			periodStart := ComputeBillingPeriodStart(userToday, budget.BillingCutoffDay, budget.BillingPeriodMonths)
			expenses, fetchErr = fetchExpensesForSummary(budgetID, periodStart)
		}
		if fetchErr != nil {
			return fmt.Errorf("fetch expenses: %w", fetchErr)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return errInternal(c, "failed to fetch summary data")
	}

	// 4. Fetch categories for all sections (needs section IDs from above).
	sectionIDs := make([]string, len(sections))
	for i, s := range sections {
		sectionIDs[i] = s.ID.String()
	}

	var categories []models.Category
	if len(sectionIDs) > 0 {
		categories, err = fetchCategoriesForSections(sectionIDs)
		if err != nil {
			return errInternal(c, "failed to fetch categories")
		}
	}

	// 5. Index categories by parent section ID. Pre-size the map to avoid
	//    rehashing for budgets with many sections.
	catsBySection := make(map[uuid.UUID][]models.Category, len(sections))
	for _, cat := range categories {
		catsBySection[cat.CategoryID] = append(catsBySection[cat.CategoryID], cat)
	}

	// 6. Aggregate expenses by category_id AND by user at each level.
	type expenseAgg struct {
		totalSpent float64
		count      int
		byUser     map[uuid.UUID]float64
	}
	expBySubcat := make(map[uuid.UUID]*expenseAgg, len(categories))
	budgetByUser := make(map[uuid.UUID]float64)
	allUserIDs := make(map[uuid.UUID]struct{})

	for _, exp := range expenses {
		agg := expBySubcat[exp.CategoryID]
		if agg == nil {
			agg = &expenseAgg{byUser: make(map[uuid.UUID]float64)}
			expBySubcat[exp.CategoryID] = agg
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

	// 6b. Batch-fetch profiles for all users who have expenses.
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
		profileBody, profileStatus, profileErr := database.DB.Get("profiles", profileQuery)
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

	// 7. Build the response.
	totalBudget := roundAmount(budget.MonthlyIncome)

	var totalSpent float64
	for _, exp := range expenses {
		totalSpent += exp.Amount
	}
	totalSpent = roundAmount(totalSpent)

	sectionSummaries := make([]models.SectionSummary, 0, len(sections))
	for _, section := range sections {
		sectionAllocated := roundAmount(section.AllocationValue)

		cats := catsBySection[section.ID]
		catSummaries := make([]models.CategorySummary, 0, len(cats))
		var sectionSpent float64
		sectionByUser := make(map[uuid.UUID]float64)

		for _, cat := range cats {
			catAllocated := roundAmount(cat.AllocationValue)

			var catSpent float64
			var catCount int
			var catUserSpending []models.UserSpending
			if agg, ok := expBySubcat[cat.ID]; ok {
				catSpent = roundAmount(agg.totalSpent)
				catCount = agg.count
				catUserSpending = buildUserSpending(agg.byUser)
				// Roll up to section level.
				for uid, amt := range agg.byUser {
					sectionByUser[uid] += amt
				}
			}
			sectionSpent += catSpent

			catSummaries = append(catSummaries, models.CategorySummary{
				Category: models.SummaryCategoryView{
					ID:                cat.ID,
					SectionID:         cat.CategoryID,
					Name:              cat.Name,
					AllocationValue: cat.AllocationValue,
					Icon:              cat.Icon,
					SortOrder:         cat.SortOrder,
					CreatedAt:         cat.CreatedAt,
				},
				AllocatedAmount: catAllocated,
				TotalSpent:      catSpent,
				ExpenseCount:    catCount,
				SpendingByUser:  catUserSpending,
			})
		}

		sectionSpent = roundAmount(sectionSpent)

		sectionSummaries = append(sectionSummaries, models.SectionSummary{
			Section:         section,
			Categories:      catSummaries,
			AllocatedAmount: sectionAllocated,
			TotalSpent:      sectionSpent,
			SpendingByUser:  buildUserSpending(sectionByUser),
		})
	}

	// 8. Fetch and aggregate linked sections from other budgets.
	linkedSummaries := buildLinkedSections(budgetID, userID, userToday, profileMap, buildUserSpending)
	for _, ls := range linkedSummaries {
		totalSpent = roundAmount(totalSpent + ls.TotalSpent)
	}

	resp := models.BudgetSummary{
		Budget:         *budget,
		Sections:       sectionSummaries,
		LinkedSections: linkedSummaries,
		TotalBudget:    totalBudget,
		TotalSpent:     totalSpent,
		SpendingByUser: buildUserSpending(budgetByUser),
	}

	return c.JSON(resp)
}

// ---------- GetBudgetTrends ----------

// GetBudgetTrends returns daily spending data grouped by section. All
// computation is done in Go; the database is used purely as storage.
//
// Sections, categories, and expenses are fetched with maximum concurrency:
// sections and expenses run in parallel, then categories are fetched once
// section IDs are known.
func GetBudgetTrends(c *fiber.Ctx) error {
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

	// Fetch sections and all expenses concurrently -- they are independent.
	var (
		sections    []models.Section
		allExpenses []models.Expense
	)

	g, _ := errgroup.WithContext(c.Context())

	g.Go(func() error {
		var fetchErr error
		sections, fetchErr = fetchSections(budgetID)
		if fetchErr != nil {
			return fmt.Errorf("fetch sections: %w", fetchErr)
		}
		return nil
	})

	g.Go(func() error {
		var fetchErr error
		// For trends we only need category_id, amount, and expense_date.
		allExpenses, fetchErr = fetchExpensesForTrends(budgetID)
		if fetchErr != nil {
			return fmt.Errorf("fetch expenses: %w", fetchErr)
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return errInternal(c, "failed to fetch trends data")
	}

	// Build section ID list and fetch categories (depends on sections).
	sectionIDs := make([]string, len(sections))
	for i, s := range sections {
		sectionIDs[i] = s.ID.String()
	}

	var categories []models.Category
	if len(sectionIDs) > 0 {
		var err error
		categories, err = fetchCategoriesForSections(sectionIDs)
		if err != nil {
			return errInternal(c, "failed to fetch categories")
		}
	}

	// Map category -> parent section.
	subcatToSection := make(map[uuid.UUID]uuid.UUID, len(categories))
	for _, cat := range categories {
		subcatToSection[cat.ID] = cat.CategoryID
	}

	// Aggregate: section -> date -> total_spent.
	sectionDailyMap := make(map[uuid.UUID]map[string]float64, len(sections))
	for _, exp := range allExpenses {
		sectionID, ok := subcatToSection[exp.CategoryID]
		if !ok {
			continue
		}
		if sectionDailyMap[sectionID] == nil {
			sectionDailyMap[sectionID] = make(map[string]float64)
		}
		// Use expense_date directly (already YYYY-MM-DD string).
		sectionDailyMap[sectionID][exp.ExpenseDate] += exp.Amount
	}

	// Build response preserving section sort order.
	sectionTrends := make([]models.SectionTrend, 0, len(sections))
	for _, section := range sections {
		dailyMap := sectionDailyMap[section.ID]
		months := make([]models.MonthlyTrend, 0, len(dailyMap))
		for date, spent := range dailyMap {
			months = append(months, models.MonthlyTrend{
				Month:      date,
				TotalSpent: roundAmount(spent),
			})
		}
		// Sort months chronologically.
		sortMonthlyTrends(months)

		sectionTrends = append(sectionTrends, models.SectionTrend{
			SectionID:   section.ID,
			SectionName: section.Name,
			Months:      months,
		})
	}

	resp := models.TrendsResponse{
		BudgetID: budgetID,
		Sections: sectionTrends,
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

	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	budget, err := fetchBudget(budgetID)
	if err != nil || budget == nil {
		return errInternal(c, "failed to fetch budget")
	}

	userToday := resolveUserToday(c)

	// One-time budgets: single period from creation to now.
	if budget.BillingPeriodMonths == 0 {
		allExpenses, err := fetchAllExpensesNoRetention(budgetID)
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
	income := budget.MonthlyIncome

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
	allExpenses, err := fetchExpensesInDateRange(budgetID, oldestStart, currentStart)
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

// ---------- Linked sections helper ----------

// buildLinkedSections fetches and aggregates linked sections for a target budget.
// It reuses the same fetchSections/fetchCategoriesForSections/fetch*Expenses helpers.
func buildLinkedSections(
	targetBudgetID, userID uuid.UUID,
	userToday time.Time,
	profileMap map[uuid.UUID]*models.Profile,
	buildUserSpending func(map[uuid.UUID]float64) []models.UserSpending,
) []models.LinkedSectionSummary {
	if database.DB == nil || database.DB.Pool == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Fetch all links for this target budget.
	rows, err := database.DB.Pool.Query(ctx, `
		SELECT id, source_budget_id, target_budget_id, source_section_id,
		       source_category_id, target_section_id, filter_mode, created_by, created_at
		FROM budget_links WHERE target_budget_id = $1
	`, targetBudgetID)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var links []models.BudgetLink
	for rows.Next() {
		var l models.BudgetLink
		if err := rows.Scan(&l.ID, &l.SourceBudgetID, &l.TargetBudgetID,
			&l.SourceSectionID, &l.SourceCategoryID, &l.TargetSectionID,
			&l.FilterMode, &l.CreatedBy, &l.CreatedAt); err != nil {
			continue
		}
		links = append(links, l)
	}
	if len(links) == 0 {
		return nil
	}

	// Group links by source budget and cache fetched budgets.
	budgetCache := make(map[uuid.UUID]*models.Budget)
	result := make([]models.LinkedSectionSummary, 0, len(links))

	for _, link := range links {
		// Fetch source budget (cached).
		srcBudget, ok := budgetCache[link.SourceBudgetID]
		if !ok {
			fetched, fetchErr := fetchBudget(link.SourceBudgetID)
			if fetchErr != nil || fetched == nil {
				continue
			}
			budgetCache[link.SourceBudgetID] = fetched
			srcBudget = fetched
		}

		// Fetch the linked section.
		allSections, err := fetchSections(link.SourceBudgetID)
		if err != nil {
			continue
		}
		var section *models.Section
		for i := range allSections {
			if allSections[i].ID == link.SourceSectionID {
				section = &allSections[i]
				break
			}
		}
		if section == nil {
			continue
		}

		// Fetch categories for this section.
		cats, err := fetchCategoriesForSections([]string{section.ID.String()})
		if err != nil {
			continue
		}

		// If single-category link, filter to only that category.
		if link.SourceCategoryID != nil {
			filtered := make([]models.Category, 0, 1)
			for _, c := range cats {
				if c.ID == *link.SourceCategoryID {
					filtered = append(filtered, c)
					break
				}
			}
			cats = filtered
		}

		// Fetch expenses for the source budget's current billing period.
		var expenses []models.Expense
		if srcBudget.BillingPeriodMonths == 0 {
			expenses, _ = fetchAllExpensesNoRetention(link.SourceBudgetID)
		} else {
			periodStart := ComputeBillingPeriodStart(userToday, srcBudget.BillingCutoffDay, srcBudget.BillingPeriodMonths)
			expenses, _ = fetchExpensesForSummary(link.SourceBudgetID, periodStart)
		}

		// Build a set of category IDs in this link.
		catIDSet := make(map[uuid.UUID]bool, len(cats))
		for _, c := range cats {
			catIDSet[c.ID] = true
		}

		// Filter expenses to linked categories + apply filter_mode.
		type expAgg struct {
			totalSpent float64
			count      int
			byUser     map[uuid.UUID]float64
		}
		expBySubcat := make(map[uuid.UUID]*expAgg, len(cats))
		var linkTotalSpent float64
		linkByUser := make(map[uuid.UUID]float64)

		for _, exp := range expenses {
			if !catIDSet[exp.CategoryID] {
				continue
			}
			// Apply filter mode.
			if link.FilterMode == "mine" && (exp.CreatedBy == nil || *exp.CreatedBy != userID) {
				continue
			}

			agg := expBySubcat[exp.CategoryID]
			if agg == nil {
				agg = &expAgg{byUser: make(map[uuid.UUID]float64)}
				expBySubcat[exp.CategoryID] = agg
			}
			agg.totalSpent += exp.Amount
			agg.count++
			linkTotalSpent += exp.Amount

			if exp.CreatedBy != nil {
				uid := *exp.CreatedBy
				agg.byUser[uid] += exp.Amount
				linkByUser[uid] += exp.Amount
			}
		}

		// Build category summaries.
		catSummaries := make([]models.CategorySummary, 0, len(cats))
		for _, cat := range cats {
			catAllocated := roundAmount(cat.AllocationValue)
			var catSpent float64
			var catCount int
			var catUserSpending []models.UserSpending
			if agg, ok := expBySubcat[cat.ID]; ok {
				catSpent = roundAmount(agg.totalSpent)
				catCount = agg.count
				catUserSpending = buildUserSpending(agg.byUser)
			}
			catSummaries = append(catSummaries, models.CategorySummary{
				Category: models.SummaryCategoryView{
					ID:                cat.ID,
					SectionID:         cat.CategoryID,
					Name:              cat.Name,
					AllocationValue: cat.AllocationValue,
					Icon:              cat.Icon,
					SortOrder:         cat.SortOrder,
					CreatedAt:         cat.CreatedAt,
				},
				AllocatedAmount: catAllocated,
				TotalSpent:      catSpent,
				ExpenseCount:    catCount,
				SpendingByUser:  catUserSpending,
			})
		}

		result = append(result, models.LinkedSectionSummary{
			Link:           link,
			SourceBudget:   *srcBudget,
			Section:        *section,
			Categories:     catSummaries,
			TotalSpent:     roundAmount(linkTotalSpent),
			SpendingByUser: buildUserSpending(linkByUser),
		})
	}

	return result
}

// ---------- Data-fetching helpers ----------

// fetchBudget loads a single budget by ID. Returns nil if not found.
func fetchBudget(budgetID uuid.UUID) (*models.Budget, error) {
	query := database.NewFilter().
		Select("*").
		Eq("id", budgetID.String()).
		Build()

	body, statusCode, err := database.DB.Get("budgets", query)
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

// fetchSections loads all sections for a budget, ordered by sort_order.
func fetchSections(budgetID uuid.UUID) ([]models.Section, error) {
	query := database.NewFilter().
		Select("*").
		Eq("budget_id", budgetID.String()).
		Order("sort_order", "asc").
		Build()

	body, statusCode, err := database.DB.Get("budget_categories", query)
	if err != nil {
		return nil, err
	}
	if statusCode != http.StatusOK {
		return nil, nil
	}

	var sections []models.Section
	if err := json.Unmarshal(body, &sections); err != nil {
		return nil, err
	}
	return sections, nil
}

// fetchCategoriesForSections loads all categories whose parent section ID is
// in the provided list, ordered by sort_order.
func fetchCategoriesForSections(sectionIDs []string) ([]models.Category, error) {
	query := database.NewFilter().
		Select("*").
		In("category_id", sectionIDs).
		Order("sort_order", "asc").
		Build()

	body, statusCode, err := database.DB.Get("budget_subcategories", query)
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
		Select("subcategory_id,amount,created_by").
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
	query := database.NewFilter().
		Select("subcategory_id,amount,created_by").
		Eq("budget_id", budgetID.String()).
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

// fetchExpensesForSummary loads only the columns needed for budget summary
// aggregation (category_id, amount) within the current billing period.
// Fetching fewer columns reduces data transfer for budgets with many expenses.
func fetchExpensesForSummary(budgetID uuid.UUID, periodStart time.Time) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("subcategory_id,amount,created_by").
		Eq("budget_id", budgetID.String()).
		Gte("expense_date", periodStart.Format("2006-01-02")).
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

// fetchExpensesInDateRange loads expenses for a budget where expense_date is
// between start (inclusive) and end (exclusive).
func fetchExpensesInDateRange(budgetID uuid.UUID, start, end time.Time) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("subcategory_id,amount,expense_date").
		Eq("budget_id", budgetID.String()).
		Gte("expense_date", start.Format("2006-01-02")).
		Lt("expense_date", end.Format("2006-01-02")).
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

// fetchExpensesForTrends loads only the columns needed for trends aggregation
// (category_id, amount, expense_date) for all time. Ordering is unnecessary
// since we aggregate by date in Go.
func fetchExpensesForTrends(budgetID uuid.UUID) ([]models.Expense, error) {
	query := database.NewFilter().
		Select("subcategory_id,amount,expense_date").
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
