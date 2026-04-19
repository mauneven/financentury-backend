package handlers

import (
	"math"
	"testing"
	"time"

	"github.com/the-financial-workspace/backend/internal/models"
)

// ---------- Billing Period Calculation Tests ----------

func TestComputeBillingPeriodStart_CutoffDay1_SingleMonth(t *testing.T) {
	// Cutoff day 1 means the period starts on the 1st. If today is the 15th of
	// April, the period started April 1st.
	today := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 1, 1)
	want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=1, today=Apr15: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_CutoffDay1_TodayIsCutoff(t *testing.T) {
	// Today IS the cutoff day (day >= clampedDay), period starts today.
	today := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 1, 1)
	want := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=1, today=Apr1: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_CutoffDay15_BeforeCutoff(t *testing.T) {
	// Cutoff day 15, today is the 10th -> period started on March 15.
	today := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 15, 1)
	want := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=15, today=Apr10: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_CutoffDay15_AfterCutoff(t *testing.T) {
	// Cutoff day 15, today is the 20th -> period started April 15.
	today := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 15, 1)
	want := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=15, today=Apr20: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_CutoffDay15_ExactlyCutoff(t *testing.T) {
	// Today equals the cutoff day exactly (day >= clampedDay).
	today := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 15, 1)
	want := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=15, today=Apr15: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_CutoffDay31_February(t *testing.T) {
	// Cutoff day 31, today = March 5 -> goes back to Feb; Feb 2026 has 28 days.
	today := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 31, 1)
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=31, today=Mar5: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_CutoffDay31_LeapYear(t *testing.T) {
	// Leap year: cutoff 31, today = March 5, 2028 -> Feb has 29 days.
	today := time.Date(2028, 3, 5, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 31, 1)
	want := time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=31, today=Mar5 2028 (leap): got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_CutoffDay30_April(t *testing.T) {
	// Cutoff day 30, today = April 30 -> period starts April 30 (day >= clamped).
	today := time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 30, 1)
	want := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=30, today=Apr30: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_JanuaryRollbackToDecember(t *testing.T) {
	// Cutoff day 15, today = Jan 10, 2026 -> rolls back to Dec 15, 2025.
	today := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 15, 1)
	want := time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=15, today=Jan10: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_CutoffDay31_InFebruary(t *testing.T) {
	// Today is Feb 15, cutoff = 31 -> Feb has 28 days, today (15) < 28, so
	// go back to January and clamp 31 to 31 (Jan has 31 days).
	today := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 31, 1)
	want := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=31, today=Feb15: got %v, want %v", got, want)
	}
}

// ---------- Multi-month billing period tests ----------

func TestComputeBillingPeriodStart_MultiMonth_2Months(t *testing.T) {
	// 2-month period, cutoff day 1, today = April 15.
	// Single-month start = April 1; shift back 1 month -> March 1.
	today := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 1, 2)
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=1, period=2, today=Apr15: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_MultiMonth_3Months(t *testing.T) {
	// 3-month period, cutoff day 15, today = April 20.
	// Single-month start = April 15; shift back 2 months -> Feb 15.
	today := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 15, 3)
	want := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=15, period=3, today=Apr20: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_MultiMonth_CrossesYearBoundary(t *testing.T) {
	// 3-month period, cutoff day 1, today = Feb 15, 2026.
	// Single-month start = Feb 1; shift back 2 months -> Dec 1, 2025.
	today := time.Date(2026, 2, 15, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 1, 3)
	want := time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=1, period=3, today=Feb15: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_MultiMonth_31ClampsInTarget(t *testing.T) {
	// 2-month period, cutoff day 31, today = March 31, 2026.
	// Single-month start = March 31; shift back 1 month -> Feb 28 (clamped).
	today := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 31, 2)
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=31, period=2, today=Mar31: got %v, want %v", got, want)
	}
}

func TestComputeBillingPeriodStart_MultiMonth_BeforeCutoff(t *testing.T) {
	// 2-month period, cutoff day 15, today = April 10.
	// Before cutoff, single-month start = March 15; shift back 1 -> Feb 15.
	today := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 15, 2)
	want := time.Date(2026, 2, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoff=15, period=2, today=Apr10: got %v, want %v", got, want)
	}
}

// ---------- Edge cases ----------

func TestComputeBillingPeriodStart_DefaultsForInvalidInputs(t *testing.T) {
	today := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	// Period months <= 0 defaults to 1.
	got := ComputeBillingPeriodStart(today, 1, 0)
	want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("periodMonths=0: got %v, want %v", got, want)
	}

	// Cutoff day <= 0 defaults to 1.
	got = ComputeBillingPeriodStart(today, 0, 1)
	want = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("cutoffDay=0: got %v, want %v", got, want)
	}

	// Negative period months defaults to 1.
	got = ComputeBillingPeriodStart(today, 1, -5)
	want = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("periodMonths=-5: got %v, want %v", got, want)
	}
}

// ---------- Allocation Math Tests ----------

func TestRoundAmount(t *testing.T) {
	cases := []struct {
		input float64
		want  float64
	}{
		{0, 0},
		{1234.5678, 1234.57},
		{-99.999, -100.0},
		{0.001, 0.0},
		{100.0, 100.0},
		{3.456, 3.46},
		{2.344, 2.34},
		{99.995, 100.0},
	}
	for _, tc := range cases {
		got := roundAmount(tc.input)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("roundAmount(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestAllocationMath_FlatCategory(t *testing.T) {
	// In the flat model each category holds a direct monetary allocation.
	monthlyIncome := 5000000.0

	// A category allocated 22.5% of income equals 1,125,000 COP.
	catAllocated := roundAmount(monthlyIncome * 22.5 / 100)
	if catAllocated != 1125000 {
		t.Errorf("flat category allocated: got %v, want 1125000", catAllocated)
	}
}

func TestAllocationMath_ZeroIncome(t *testing.T) {
	monthlyIncome := 0.0
	allocated := roundAmount(monthlyIncome * 50 / 100)
	if allocated != 0 {
		t.Errorf("zero income category: got %v, want 0", allocated)
	}
}

func TestAllocationMath_ZeroAllocationPercent(t *testing.T) {
	monthlyIncome := 5000000.0
	allocated := roundAmount(monthlyIncome * 0 / 100)
	if allocated != 0 {
		t.Errorf("zero allocation category: got %v, want 0", allocated)
	}
}

func TestAllocationMath_SmallIncome(t *testing.T) {
	monthlyIncome := 1.0 // 1 COP
	allocated := roundAmount(monthlyIncome * 50 / 100)
	if allocated != 0.5 {
		t.Errorf("small income category: got %v, want 0.5", allocated)
	}
}

// ---------- Template Categories Validation ----------

// TestAllTemplates_CategoriesSumTo100 verifies every template's flat category
// percentages add up to 100.
func TestAllTemplates_CategoriesSumTo100(t *testing.T) {
	templates := map[string][]guidedCategory{
		"balanced":    getBalancedCategories(),
		"debt-free":   getDebtFreeCategories(),
		"debt-payoff": getDebtPayoffCategories(),
		"travel":      getTravelCategories(),
		"event":       getEventCategories(),
	}
	for name, cats := range templates {
		var sum float64
		for _, c := range cats {
			sum += c.Percent
		}
		if math.Abs(sum-100) > 0.01 {
			t.Errorf("%s: categories sum to %f, want 100", name, sum)
		}
	}
}

// TestAllTemplates_UsesLucideIcons verifies all icons are short key strings.
func TestAllTemplates_UsesLucideIcons(t *testing.T) {
	templates := map[string][]guidedCategory{
		"balanced":    getBalancedCategories(),
		"debt-free":   getDebtFreeCategories(),
		"debt-payoff": getDebtPayoffCategories(),
		"travel":      getTravelCategories(),
		"event":       getEventCategories(),
	}
	for name, cats := range templates {
		for _, c := range cats {
			if len(c.Icon) > 20 || containsEmoji(c.Icon) {
				t.Errorf("%s / category %s icon should be a lucide key string, got %q", name, c.Name, c.Icon)
			}
		}
	}
}

func TestBalancedCategories_Structure(t *testing.T) {
	cats := getBalancedCategories()

	if len(cats) == 0 {
		t.Fatal("balanced template should have at least one category")
	}
	if len(cats) > maxCategoriesPerBudget {
		t.Errorf("balanced template has %d categories, exceeds cap of %d", len(cats), maxCategoriesPerBudget)
	}

	// Each category must have a non-empty name and a positive percent.
	for _, c := range cats {
		if c.Name == "" {
			t.Error("balanced category has empty name")
		}
		if c.Percent <= 0 {
			t.Errorf("balanced category %q has non-positive percent %v", c.Name, c.Percent)
		}
	}
}

// ---------- daysIn Tests ----------

func TestDaysIn(t *testing.T) {
	cases := []struct {
		year  int
		month time.Month
		want  int
	}{
		{2026, time.January, 31},
		{2026, time.February, 28},
		{2028, time.February, 29}, // leap year
		{2026, time.April, 30},
		{2026, time.June, 30},
		{2026, time.December, 31},
	}
	for _, tc := range cases {
		got := daysIn(tc.year, tc.month)
		if got != tc.want {
			t.Errorf("daysIn(%d, %v) = %d, want %d", tc.year, tc.month, got, tc.want)
		}
	}
}

// ---------- minInt Tests ----------

func TestMinInt(t *testing.T) {
	if minInt(3, 5) != 3 {
		t.Error("minInt(3,5) should be 3")
	}
	if minInt(5, 3) != 3 {
		t.Error("minInt(5,3) should be 3")
	}
	if minInt(3, 3) != 3 {
		t.Error("minInt(3,3) should be 3")
	}
	if minInt(-1, 0) != -1 {
		t.Error("minInt(-1,0) should be -1")
	}
}

// ---------- sortMonthlyTrends Test ----------

func TestSortMonthlyTrends(t *testing.T) {
	trends := []models.MonthlyTrend{
		{Month: "2026-04-15", TotalSpent: 100},
		{Month: "2026-01-05", TotalSpent: 200},
		{Month: "2026-03-10", TotalSpent: 300},
		{Month: "2025-12-01", TotalSpent: 400},
	}

	sortMonthlyTrends(trends)

	expected := []string{"2025-12-01", "2026-01-05", "2026-03-10", "2026-04-15"}
	for i, want := range expected {
		if trends[i].Month != want {
			t.Errorf("index %d: got %v, want %v", i, trends[i].Month, want)
		}
	}
}

func TestSortMonthlyTrends_Empty(t *testing.T) {
	var trends []models.MonthlyTrend
	sortMonthlyTrends(trends) // should not panic
}

func TestSortMonthlyTrends_SingleElement(t *testing.T) {
	trends := []models.MonthlyTrend{{Month: "2026-01-01", TotalSpent: 100}}
	sortMonthlyTrends(trends)
	if trends[0].Month != "2026-01-01" {
		t.Error("single element sort failed")
	}
}

func TestSortMonthlyTrends_AlreadySorted(t *testing.T) {
	trends := []models.MonthlyTrend{
		{Month: "2026-01-01", TotalSpent: 100},
		{Month: "2026-02-01", TotalSpent: 200},
		{Month: "2026-03-01", TotalSpent: 300},
	}
	sortMonthlyTrends(trends)
	expected := []string{"2026-01-01", "2026-02-01", "2026-03-01"}
	for i, want := range expected {
		if trends[i].Month != want {
			t.Errorf("already sorted: index %d = %v, want %v", i, trends[i].Month, want)
		}
	}
}

func TestSortMonthlyTrends_ReverseSorted(t *testing.T) {
	trends := []models.MonthlyTrend{
		{Month: "2026-03-01", TotalSpent: 300},
		{Month: "2026-02-01", TotalSpent: 200},
		{Month: "2026-01-01", TotalSpent: 100},
	}
	sortMonthlyTrends(trends)
	expected := []string{"2026-01-01", "2026-02-01", "2026-03-01"}
	for i, want := range expected {
		if trends[i].Month != want {
			t.Errorf("reverse sorted: index %d = %v, want %v", i, trends[i].Month, want)
		}
	}
	// Verify amounts followed the dates.
	if trends[0].TotalSpent != 100 || trends[2].TotalSpent != 300 {
		t.Error("amounts should follow their dates after sorting")
	}
}

func TestSortMonthlyTrends_DuplicateDates(t *testing.T) {
	trends := []models.MonthlyTrend{
		{Month: "2026-02-01", TotalSpent: 50},
		{Month: "2026-01-01", TotalSpent: 100},
		{Month: "2026-02-01", TotalSpent: 75},
	}
	sortMonthlyTrends(trends)
	if trends[0].Month != "2026-01-01" {
		t.Errorf("first should be 2026-01-01, got %v", trends[0].Month)
	}
	if trends[1].Month != "2026-02-01" || trends[2].Month != "2026-02-01" {
		t.Error("duplicate dates should both appear")
	}
}

// ---------- roundAmount Additional Edge Cases ----------

func TestRoundAmount_LargeValues(t *testing.T) {
	// Verify roundAmount handles large monetary values typical of COP.
	got := roundAmount(5000000.0 * 50.0 / 100.0)
	if got != 2500000.0 {
		t.Errorf("roundAmount(5000000*50/100) = %v, want 2500000", got)
	}
}

func TestRoundAmount_RepeatingDecimal(t *testing.T) {
	// 1/3 = 0.333... rounded to 2 decimal places = 0.33
	got := roundAmount(1.0 / 3.0)
	if got != 0.33 {
		t.Errorf("roundAmount(1/3) = %v, want 0.33", got)
	}
}

func TestRoundAmount_HalfCent(t *testing.T) {
	// 0.005 should round to 0.01 (banker's rounding in math.Round)
	got := roundAmount(0.005)
	if got != 0.01 {
		t.Errorf("roundAmount(0.005) = %v, want 0.01", got)
	}
}

// ---------- shiftMonths Tests ----------

func TestShiftMonths_Forward(t *testing.T) {
	d := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, 3, 15)
	want := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths forward 3: got %v, want %v", got, want)
	}
}

func TestShiftMonths_BackwardAcrossYear(t *testing.T) {
	d := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, -3, 1)
	want := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths back 3 across year: got %v, want %v", got, want)
	}
}

func TestShiftMonths_ClampToShorterMonth(t *testing.T) {
	// Shifting from Jan 31 forward to Feb should clamp to Feb 28.
	d := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, 1, 31)
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths Jan31 -> Feb: got %v, want %v", got, want)
	}
}

func TestShiftMonths_ZeroShift(t *testing.T) {
	d := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, 0, 15)
	want := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths zero: got %v, want %v", got, want)
	}
}

func TestShiftMonths_BackMultipleYears(t *testing.T) {
	d := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, -24, 1) // 2 years back
	want := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths back 24: got %v, want %v", got, want)
	}
}

// ---------- Helpers ----------

// containsEmoji does a simple heuristic check: if any rune is above the Basic
// Latin + Latin Supplement range, it's likely an emoji.
func containsEmoji(s string) bool {
	for _, r := range s {
		if r > 0x024F { // beyond Latin Extended-B
			return true
		}
	}
	return false
}
