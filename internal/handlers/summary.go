package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
)

// ---------- Billing-period helpers (exported for testing) ----------

// ComputeBillingPeriodStart calculates the start date of the current billing
// period given the budget's cutoff day and period length in months.
//
// Algorithm (ported from the Supabase RPC get_budget_summary):
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

// ---------- GetBudgetSummary ----------

// GetBudgetSummary computes and returns the full budget summary. All math is
// done in Go; Supabase is used purely as storage via PostgREST.
func GetBudgetSummary(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	// 1. Verify access (owner or collaborator).
	if err := verifyBudgetAccess(budgetID, userID); err != nil {
		return errNotFound(c, "budget not found")
	}

	// 2. Fetch the budget.
	budget, err := fetchBudget(budgetID)
	if err != nil {
		return errInternal(c, "failed to fetch budget")
	}
	if budget == nil {
		return errNotFound(c, "budget not found")
	}

	// 3. Compute billing period start.
	today := time.Now().UTC()
	periodStart := ComputeBillingPeriodStart(today, budget.BillingCutoffDay, budget.BillingPeriodMonths)

	// 4. Fetch sections (budget_categories).
	sections, err := fetchSections(budgetID)
	if err != nil {
		return errInternal(c, "failed to fetch sections")
	}

	// 5. Fetch categories (budget_subcategories) for all sections.
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

	// Index categories by parent section ID.
	catsBySection := make(map[uuid.UUID][]models.Category)
	for _, cat := range categories {
		catsBySection[cat.CategoryID] = append(catsBySection[cat.CategoryID], cat)
	}

	// 6. Fetch expenses in current billing period.
	expenses, err := fetchExpensesInPeriod(budgetID, periodStart)
	if err != nil {
		return errInternal(c, "failed to fetch expenses")
	}

	// Aggregate expenses by subcategory_id: total_spent and count.
	type expenseAgg struct {
		totalSpent float64
		count      int
	}
	expBySubcat := make(map[uuid.UUID]*expenseAgg)
	for _, exp := range expenses {
		agg := expBySubcat[exp.CategoryID]
		if agg == nil {
			agg = &expenseAgg{}
			expBySubcat[exp.CategoryID] = agg
		}
		agg.totalSpent += exp.Amount
		agg.count++
	}

	// 7. Build the response.
	var totalBudget, totalSpent float64

	sectionSummaries := make([]models.SectionSummary, 0, len(sections))
	for _, section := range sections {
		sectionAllocated := roundAmount(budget.MonthlyIncome * section.AllocationPercent / 100)
		totalBudget += sectionAllocated

		cats := catsBySection[section.ID]
		catSummaries := make([]models.CategorySummary, 0, len(cats))
		var sectionSpent float64

		for _, cat := range cats {
			catAllocated := roundAmount(sectionAllocated * cat.AllocationPercent / 100)

			var catSpent float64
			var catCount int
			if agg, ok := expBySubcat[cat.ID]; ok {
				catSpent = roundAmount(agg.totalSpent)
				catCount = agg.count
			}
			sectionSpent += catSpent

			catSummaries = append(catSummaries, models.CategorySummary{
				Category: models.SummaryCategoryView{
					ID:                cat.ID,
					SectionID:         cat.CategoryID,
					Name:              cat.Name,
					AllocationPercent: cat.AllocationPercent,
					Icon:              cat.Icon,
					SortOrder:         cat.SortOrder,
					CreatedAt:         cat.CreatedAt,
				},
				AllocatedAmount: catAllocated,
				TotalSpent:      catSpent,
				ExpenseCount:    catCount,
			})
		}

		sectionSpent = roundAmount(sectionSpent)
		totalSpent += sectionSpent

		sectionSummaries = append(sectionSummaries, models.SectionSummary{
			Section:         section,
			Categories:      catSummaries,
			AllocatedAmount: sectionAllocated,
			TotalSpent:      sectionSpent,
		})
	}

	totalBudget = roundAmount(totalBudget)
	totalSpent = roundAmount(totalSpent)

	resp := models.BudgetSummary{
		Budget:      *budget,
		Sections:    sectionSummaries,
		TotalBudget: totalBudget,
		TotalSpent:  totalSpent,
	}

	return c.JSON(resp)
}

// ---------- GetBudgetTrends ----------

// GetBudgetTrends returns daily spending data grouped by section. All
// computation is done in Go; Supabase is used purely as storage.
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

	// Fetch sections.
	sections, err := fetchSections(budgetID)
	if err != nil {
		return errInternal(c, "failed to fetch sections")
	}

	// Build section ID -> name map and collect all section IDs.
	sectionIDs := make([]string, len(sections))
	for i, s := range sections {
		sectionIDs[i] = s.ID.String()
	}

	// Fetch all categories to map subcategory -> section.
	var categories []models.Category
	if len(sectionIDs) > 0 {
		categories, err = fetchCategoriesForSections(sectionIDs)
		if err != nil {
			return errInternal(c, "failed to fetch categories")
		}
	}
	subcatToSection := make(map[uuid.UUID]uuid.UUID)
	for _, cat := range categories {
		subcatToSection[cat.ID] = cat.CategoryID
	}

	// Fetch ALL expenses for this budget (no period filter for trends).
	allExpenses, err := fetchAllExpenses(budgetID)
	if err != nil {
		return errInternal(c, "failed to fetch expenses")
	}

	// Aggregate: section -> date -> total_spent.
	sectionDailyMap := make(map[uuid.UUID]map[string]float64)
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
