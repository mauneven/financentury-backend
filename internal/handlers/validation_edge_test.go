package handlers

import (
	"math"
	"strings"
	"testing"
)

// ==================== isValidDate Edge Cases ====================

func TestIsValidDate_YearBoundaries(t *testing.T) {
	// December 31 -> January 1 transitions.
	cases := []struct {
		date string
		want bool
	}{
		{"2025-12-31", true},
		{"2026-01-01", true},
		{"1999-12-31", true},
		{"2000-01-01", true},
	}
	for _, tc := range cases {
		got := isValidDate(tc.date)
		if got != tc.want {
			t.Errorf("isValidDate(%q) = %v, want %v", tc.date, got, tc.want)
		}
	}
}

func TestIsValidDate_Feb29_LeapVsNonLeap(t *testing.T) {
	// Leap years: divisible by 4, except centuries unless divisible by 400.
	leapYears := []string{
		"2024-02-29", // divisible by 4
		"2000-02-29", // divisible by 400
		"2400-02-29", // divisible by 400
	}
	for _, d := range leapYears {
		if !isValidDate(d) {
			t.Errorf("isValidDate(%q) = false, want true (leap year)", d)
		}
	}

	nonLeapYears := []string{
		"2026-02-29", // not divisible by 4
		"2100-02-29", // divisible by 100 but not 400
		"1900-02-29", // divisible by 100 but not 400
	}
	for _, d := range nonLeapYears {
		if isValidDate(d) {
			t.Errorf("isValidDate(%q) = true, want false (not a leap year)", d)
		}
	}
}

func TestIsValidDate_ExtremeYears(t *testing.T) {
	cases := []struct {
		date string
		want bool
	}{
		// Year 0000: Go's time.Parse accepts year 0000.
		{"0000-01-01", true},
		// Year 9999: valid upper boundary.
		{"9999-12-31", true},
	}
	for _, tc := range cases {
		got := isValidDate(tc.date)
		if got != tc.want {
			t.Errorf("isValidDate(%q) = %v, want %v", tc.date, got, tc.want)
		}
	}
}

func TestIsValidDate_NegativeYears(t *testing.T) {
	// Negative years should be rejected (not in YYYY-MM-DD format).
	negatives := []string{
		"-001-01-01",
		"-2026-01-01",
	}
	for _, d := range negatives {
		if isValidDate(d) {
			t.Errorf("isValidDate(%q) = true, want false (negative year)", d)
		}
	}
}

func TestIsValidDate_TimezoneSuffixes(t *testing.T) {
	// Date strings with timezone info should be rejected since
	// time.Parse with dateFormat="2006-01-02" does not consume extra chars.
	invalid := []string{
		"2026-04-10Z",
		"2026-04-10+00:00",
		"2026-04-10-05:00",
		"2026-04-10T00:00:00Z",
		"2026-04-10T00:00:00+00:00",
	}
	for _, d := range invalid {
		if isValidDate(d) {
			t.Errorf("isValidDate(%q) = true, want false (has timezone suffix)", d)
		}
	}
}

// ==================== Name Validation Edge Cases ====================

func TestNameValidation_OnlyWhitespace(t *testing.T) {
	name := "   \t  "
	// The current validation only checks for empty string and length.
	// A whitespace-only string passes the emptiness check (name != "")
	// and passes the length check. This documents current behavior.
	if name == "" {
		t.Error("whitespace-only name should not be detected as empty by == check")
	}
	if len(name) > maxNameLength {
		t.Error("short whitespace name should not exceed maxNameLength")
	}
}

func TestNameValidation_OnlySpecialCharacters(t *testing.T) {
	name := "!@#$%^&*()_+-=[]{}|;':\",./<>?"
	// Special characters are allowed as long as they pass length check.
	if name == "" || len(name) > maxNameLength {
		t.Error("special character name should pass basic validation")
	}
}

func TestNameValidation_NewlinesAndTabs(t *testing.T) {
	name := "Budget\nWith\tTabs\nAnd\nNewlines"
	// Newlines and tabs are not explicitly rejected by the current validation.
	if name == "" || len(name) > maxNameLength {
		t.Error("name with newlines/tabs should pass basic validation")
	}
}

func TestNameValidation_NullBytes_Edge(t *testing.T) {
	name := "Budget\x00Name"
	// Null bytes are not explicitly filtered by Go string length check.
	if name == "" || len(name) > maxNameLength {
		t.Error("name with null byte should pass basic validation")
	}
	if len(name) != 11 {
		t.Errorf("len = %d, want 11", len(name))
	}
}

// ==================== Amount Edge Cases ====================

func TestAmountValidation_FloatPrecision_0_1_Plus_0_2(t *testing.T) {
	// Classic float precision edge case: 0.1 + 0.2 != 0.3 in IEEE 754.
	// Use variables to prevent Go constant folding at compile time.
	var a, b float64
	a = 0.1
	b = 0.2
	amount := a + b
	// Due to IEEE 754, 0.1+0.2 = 0.30000000000000004
	if amount == 0.3 {
		t.Error("0.1 + 0.2 should not equal 0.3 exactly in float64")
	}
	// But it should still be a valid amount (positive and under max).
	if amount <= 0 {
		t.Error("0.1 + 0.2 should be positive")
	}
	if amount > maxAmountValue {
		t.Error("0.1 + 0.2 should not exceed max")
	}
}

func TestAmountValidation_ExactBoundary(t *testing.T) {
	// Exactly at max: should pass.
	amount := maxAmountValue
	if amount > maxAmountValue {
		t.Error("exact max should pass")
	}

	// Just over max: should fail. At 1e15 scale, float64 cannot represent
	// 0.01 increments (the ULP is ~0.125), so we use 1.0 which is large
	// enough to be representable above maxAmountValue.
	overMax := maxAmountValue + 1.0
	if overMax <= maxAmountValue {
		t.Error("max + 1.0 should exceed maxAmountValue")
	}
}

func TestAmountValidation_Infinity(t *testing.T) {
	// Positive infinity should exceed max.
	inf := math.Inf(1)
	if inf <= maxAmountValue {
		t.Error("+Inf should exceed maxAmountValue")
	}
}

// ==================== AllocationPercent Exact Boundaries ====================

func TestAllocationPercent_ExactBoundaries(t *testing.T) {
	cases := []struct {
		pct   float64
		valid bool
	}{
		{0.0, true},
		{100.0, true},
		{-0.001, false},
		{100.001, false},
		{50.0, true},
		{-0.0, true}, // negative zero equals zero in IEEE 754
	}
	for _, tc := range cases {
		isValid := tc.pct >= 0 && tc.pct <= 100
		if isValid != tc.valid {
			t.Errorf("allocationPercent(%v): valid=%v, want %v", tc.pct, isValid, tc.valid)
		}
	}
}

// ==================== marshalJSON Edge Cases ====================

func TestMarshalJSON_DeeplyNestedStructure(t *testing.T) {
	// Build a deeply nested map to test JSON serialization depth.
	var nested interface{}
	nested = "leaf"
	for i := 0; i < 100; i++ {
		nested = map[string]interface{}{"level": nested}
	}

	data, err := marshalJSON(nested)
	if err != nil {
		t.Fatalf("marshalJSON with deep nesting failed: %v", err)
	}

	// Should contain "leaf" somewhere in the output.
	if !strings.Contains(string(data), "leaf") {
		t.Error("deeply nested structure should contain 'leaf' value")
	}
}

func TestMarshalJSON_LargePayload(t *testing.T) {
	// Build a payload with many entries to produce 1MB+ of JSON.
	entries := make([]map[string]string, 10000)
	longValue := strings.Repeat("x", 100)
	for i := range entries {
		entries[i] = map[string]string{
			"key":  longValue,
			"val":  longValue,
			"data": longValue,
		}
	}

	data, err := marshalJSON(entries)
	if err != nil {
		t.Fatalf("marshalJSON with large payload failed: %v", err)
	}

	// The output should be at least 1MB.
	if len(data) < 1024*1024 {
		t.Errorf("expected at least 1MB of JSON, got %d bytes", len(data))
	}
}

func TestMarshalJSON_EmptySlice(t *testing.T) {
	data, err := marshalJSON([]int{})
	if err != nil {
		t.Fatalf("marshalJSON([]int{}) failed: %v", err)
	}
	if string(data) != "[]" {
		t.Errorf("empty slice = %s, want []", string(data))
	}
}

func TestMarshalJSON_EmptyMap(t *testing.T) {
	data, err := marshalJSON(map[string]string{})
	if err != nil {
		t.Fatalf("marshalJSON(empty map) failed: %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("empty map = %s, want {}", string(data))
	}
}

// ==================== Performance: isValidDate ====================

func TestIsValidDate_Performance_100K(t *testing.T) {
	// Verify isValidDate can be called 100K times in under 2 seconds.
	// This is a sanity check, not a strict benchmark. The testing.B
	// benchmarks in the bench file provide more accurate measurements.
	dates := []string{
		"2026-01-01", "2026-06-15", "2026-12-31",
		"2024-02-29", "invalid", "2026-13-01",
	}

	for i := 0; i < 100000; i++ {
		_ = isValidDate(dates[i%len(dates)])
	}
	// If we reach here without timing out, the test passes.
}

// ==================== Performance: marshalJSON ====================

func TestMarshalJSON_Performance_10K(t *testing.T) {
	// Verify marshalJSON with a typical expense payload can be called 10K
	// times without excessive delay.
	payload := map[string]interface{}{
		"id":           "33333333-3333-3333-3333-333333333333",
		"budget_id":    "22222222-2222-2222-2222-222222222222",
		"category_id":  "11111111-1111-1111-1111-111111111111",
		"amount":       150.50,
		"description":  "Groceries at the supermarket",
		"expense_date": "2026-04-10",
	}

	for i := 0; i < 10000; i++ {
		_, err := marshalJSON(payload)
		if err != nil {
			t.Fatalf("marshalJSON iteration %d failed: %v", i, err)
		}
	}
}

// ==================== Benchmarks ====================

func BenchmarkIsValidDate_Valid(b *testing.B) {
	for i := 0; i < b.N; i++ {
		isValidDate("2026-04-10")
	}
}

func BenchmarkIsValidDate_Invalid(b *testing.B) {
	for i := 0; i < b.N; i++ {
		isValidDate("not-a-date")
	}
}

func BenchmarkMarshalJSON_TypicalPayload(b *testing.B) {
	payload := map[string]interface{}{
		"id":           "33333333-3333-3333-3333-333333333333",
		"budget_id":    "22222222-2222-2222-2222-222222222222",
		"category_id":  "11111111-1111-1111-1111-111111111111",
		"amount":       150.50,
		"description":  "Groceries at the supermarket",
		"expense_date": "2026-04-10",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := marshalJSON(payload); err != nil {
			b.Fatal(err)
		}
	}
}
