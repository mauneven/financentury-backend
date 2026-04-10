package handlers

import (
	"math"
	"testing"
	"time"

	"github.com/the-financial-workspace/backend/internal/models"
)

// ==================== roundAmount Edge Cases ====================

func TestRoundAmount_VerySmallCloseToZero(t *testing.T) {
	cases := []struct {
		input float64
		want  float64
	}{
		{0.001, 0.0},
		{0.004, 0.0},
		{0.005, 0.01},  // rounds up
		{0.009, 0.01},
		{-0.001, 0.0},  // negative very small
		{-0.004, 0.0},
		{-0.005, -0.01}, // rounds away from zero
		{-0.009, -0.01},
		{1e-10, 0.0},   // extremely small positive
		{-1e-10, 0.0},  // extremely small negative
	}
	for _, tc := range cases {
		got := roundAmount(tc.input)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("roundAmount(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestRoundAmount_BankersRoundingEdge_0_005(t *testing.T) {
	// math.Round uses "round half away from zero" (not banker's rounding).
	// 0.005 * 100 = 0.5, math.Round(0.5) = 1 -> 0.01
	got := roundAmount(0.005)
	if got != 0.01 {
		t.Errorf("roundAmount(0.005) = %v, want 0.01", got)
	}

	// 0.015 * 100 = 1.5, math.Round(1.5) = 2 -> 0.02
	got = roundAmount(0.015)
	if got != 0.02 {
		t.Errorf("roundAmount(0.015) = %v, want 0.02", got)
	}

	// Note: due to float64 representation, 0.025*100 might be 2.4999...
	// which rounds to 2, giving 0.02. This is an inherent float limitation.
	got = roundAmount(0.025)
	// Accept either 0.02 or 0.03 due to float representation.
	if got != 0.02 && got != 0.03 {
		t.Errorf("roundAmount(0.025) = %v, want 0.02 or 0.03", got)
	}
}

func TestRoundAmount_NegativeNumbers(t *testing.T) {
	cases := []struct {
		input float64
		want  float64
	}{
		{-1.0, -1.0},
		{-99.999, -100.0},
		{-0.5, -0.5},
		{-1234.565, -1234.57},
		{-0.001, 0.0},
		{-1e10, -1e10},
	}
	for _, tc := range cases {
		got := roundAmount(tc.input)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("roundAmount(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestRoundAmount_VeryLargeNumbers(t *testing.T) {
	cases := []struct {
		input float64
		want  float64
	}{
		{1e15, 1e15},
		{1e14 + 0.12, 1e14 + 0.12},
		{999999999999.99, 999999999999.99},
	}
	for _, tc := range cases {
		got := roundAmount(tc.input)
		if math.Abs(got-tc.want) > 0.01 {
			t.Errorf("roundAmount(%v) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestRoundAmount_SpecialValues(t *testing.T) {
	// NaN
	nan := roundAmount(math.NaN())
	if !math.IsNaN(nan) {
		t.Errorf("roundAmount(NaN) = %v, want NaN", nan)
	}

	// Positive infinity
	posInf := roundAmount(math.Inf(1))
	if !math.IsInf(posInf, 1) {
		t.Errorf("roundAmount(+Inf) = %v, want +Inf", posInf)
	}

	// Negative infinity
	negInf := roundAmount(math.Inf(-1))
	if !math.IsInf(negInf, -1) {
		t.Errorf("roundAmount(-Inf) = %v, want -Inf", negInf)
	}

	// Negative zero
	negZero := roundAmount(math.Copysign(0, -1))
	if negZero != 0 {
		t.Errorf("roundAmount(-0) = %v, want 0", negZero)
	}
}

// ==================== sortMonthlyTrends Edge Cases ====================

func TestSortMonthlyTrends_EmptySlice(t *testing.T) {
	var trends []models.MonthlyTrend
	// Should not panic.
	sortMonthlyTrends(trends)
	if len(trends) != 0 {
		t.Error("empty slice should remain empty after sort")
	}
}

func TestSortMonthlyTrends_SingleElement_Edge(t *testing.T) {
	trends := []models.MonthlyTrend{{Month: "2026-06-15", TotalSpent: 42}}
	sortMonthlyTrends(trends)
	if trends[0].Month != "2026-06-15" || trends[0].TotalSpent != 42 {
		t.Error("single element should be unchanged")
	}
}

func TestSortMonthlyTrends_LargeSlice_10000(t *testing.T) {
	// Generate 10000 trends in reverse order.
	n := 10000
	trends := make([]models.MonthlyTrend, n)
	for i := 0; i < n; i++ {
		// Create dates in reverse: 2099-12-31 down to some earlier date.
		day := n - i
		year := 2000 + day/365
		month := (day%365)/30 + 1
		if month > 12 {
			month = 12
		}
		dom := day%28 + 1
		dateStr := time.Date(year, time.Month(month), dom, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		trends[i] = models.MonthlyTrend{Month: dateStr, TotalSpent: float64(i)}
	}

	sortMonthlyTrends(trends)

	// Verify sorted order.
	for i := 1; i < len(trends); i++ {
		if trends[i].Month < trends[i-1].Month {
			t.Errorf("not sorted at index %d: %q < %q", i, trends[i].Month, trends[i-1].Month)
			break
		}
	}
}

func TestSortMonthlyTrends_AllSameDate(t *testing.T) {
	trends := []models.MonthlyTrend{
		{Month: "2026-04-10", TotalSpent: 100},
		{Month: "2026-04-10", TotalSpent: 200},
		{Month: "2026-04-10", TotalSpent: 300},
	}
	sortMonthlyTrends(trends)
	// All dates are the same so the order of amounts is implementation-defined.
	// Just verify all elements are present.
	total := 0.0
	for _, t := range trends {
		total += t.TotalSpent
	}
	if total != 600 {
		t.Errorf("total = %v, want 600", total)
	}
}

// ==================== shiftMonths Edge Cases ====================

func TestShiftMonths_Plus120Months(t *testing.T) {
	d := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, 120, 15) // +10 years
	want := time.Date(2036, 1, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths(+120) = %v, want %v", got, want)
	}
}

func TestShiftMonths_Minus120Months(t *testing.T) {
	d := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, -120, 15) // -10 years
	want := time.Date(2016, 6, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths(-120) = %v, want %v", got, want)
	}
}

func TestShiftMonths_March31_BackToFebruary(t *testing.T) {
	// March 31 shifted back 1 month should clamp to Feb 28 (non-leap) or Feb 29 (leap).
	d := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, -1, 31)
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths(Mar31 -> Feb, non-leap) = %v, want %v", got, want)
	}

	// Leap year: March 31, 2028 back 1 month -> Feb 29.
	d = time.Date(2028, 3, 31, 0, 0, 0, 0, time.UTC)
	got = shiftMonths(d, -1, 31)
	want = time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths(Mar31 -> Feb, leap) = %v, want %v", got, want)
	}
}

func TestShiftMonths_Jan31_ForwardTo_Feb_Mar_Apr(t *testing.T) {
	d := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)

	// +1 month: Feb 28 (2026 non-leap)
	got := shiftMonths(d, 1, 31)
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Jan31 + 1 month = %v, want %v", got, want)
	}

	// +2 months: March 31
	got = shiftMonths(d, 2, 31)
	want = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Jan31 + 2 months = %v, want %v", got, want)
	}

	// +3 months: April 30 (clamped)
	got = shiftMonths(d, 3, 31)
	want = time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Jan31 + 3 months = %v, want %v", got, want)
	}
}

func TestShiftMonths_DecemberToJanuary(t *testing.T) {
	d := time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, 1, 15)
	want := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Dec -> Jan = %v, want %v", got, want)
	}
}

func TestShiftMonths_JanuaryToDecember(t *testing.T) {
	d := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, -1, 15)
	want := time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("Jan -> Dec = %v, want %v", got, want)
	}
}

func TestShiftMonths_LargeForward_240Months(t *testing.T) {
	d := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := shiftMonths(d, 240, 1) // +20 years
	want := time.Date(2046, 1, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("shiftMonths(+240) = %v, want %v", got, want)
	}
}

// ==================== ComputeBillingPeriodStart Additional Edge Cases ====================

func TestComputeBillingPeriodStart_CutoffDay31_AllMonths(t *testing.T) {
	// With cutoff day 31 and today being the last day of each month,
	// the period should start on the last day of that month.
	monthDays := map[time.Month]int{
		time.January:   31,
		time.February:  28,
		time.March:     31,
		time.April:     30,
		time.May:       31,
		time.June:      30,
		time.July:      31,
		time.August:    31,
		time.September: 30,
		time.October:   31,
		time.November:  30,
		time.December:  31,
	}

	for month, lastDay := range monthDays {
		today := time.Date(2026, month, lastDay, 12, 0, 0, 0, time.UTC)
		got := ComputeBillingPeriodStart(today, 31, 1)
		want := time.Date(2026, month, lastDay, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Errorf("month=%v, lastDay=%d: got %v, want %v", month, lastDay, got, want)
		}
	}
}

func TestComputeBillingPeriodStart_MultiMonth_12Months(t *testing.T) {
	// 12-month billing period (annual), cutoff day 1.
	today := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	got := ComputeBillingPeriodStart(today, 1, 12)
	// Single-month start = June 1; shift back 11 months -> July 1, 2025.
	want := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("12-month period: got %v, want %v", got, want)
	}
}

// ==================== Benchmarks ====================

func BenchmarkRoundAmount(b *testing.B) {
	values := []float64{0, 1234.5678, -99.999, 0.001, 100.0, 3.456, 2.344, 99.995, 5000000.50, -0.005}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, v := range values {
			_ = roundAmount(v)
		}
	}
}

func BenchmarkSortMonthlyTrends_100(b *testing.B) {
	base := make([]models.MonthlyTrend, 100)
	for i := 0; i < 100; i++ {
		day := 100 - i
		year := 2026
		month := (day%12) + 1
		dom := (day%28) + 1
		dateStr := time.Date(year, time.Month(month), dom, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		base[i] = models.MonthlyTrend{Month: dateStr, TotalSpent: float64(i)}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Copy to avoid sorting an already-sorted slice.
		trends := make([]models.MonthlyTrend, len(base))
		copy(trends, base)
		sortMonthlyTrends(trends)
	}
}

func BenchmarkShiftMonths(b *testing.B) {
	d := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = shiftMonths(d, i%24-12, 15)
	}
}

func BenchmarkComputeBillingPeriodStart(b *testing.B) {
	today := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ComputeBillingPeriodStart(today, 15, 1)
	}
}
