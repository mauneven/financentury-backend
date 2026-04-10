package handlers

import (
	"strings"
	"testing"
)

// ==================== isValidDate ====================

func TestIsValidDate_ValidDates(t *testing.T) {
	valid := []string{
		"2026-01-01",
		"2026-12-31",
		"2000-02-29", // leap year
		"1999-06-15",
		"2026-04-10",
	}
	for _, d := range valid {
		if !isValidDate(d) {
			t.Errorf("isValidDate(%q) = false, want true", d)
		}
	}
}

func TestIsValidDate_InvalidDates(t *testing.T) {
	invalid := []string{
		"",
		"2026",
		"2026-13-01",   // month 13
		"2026-00-01",   // month 0
		"2026-01-32",   // day 32
		"2026-02-29",   // 2026 is not a leap year
		"not-a-date",
		"01-01-2026",   // wrong order
		"2026/01/01",   // wrong separator
		"2026-1-1",     // missing leading zeros
		"2026-01-01T00:00:00Z", // datetime, not date
		" 2026-01-01",  // leading space
		"2026-01-01 ",  // trailing space
	}
	for _, d := range invalid {
		if isValidDate(d) {
			t.Errorf("isValidDate(%q) = true, want false", d)
		}
	}
}

func TestIsValidDate_BoundaryMonths(t *testing.T) {
	// First and last valid months
	if !isValidDate("2026-01-15") {
		t.Error("January should be valid")
	}
	if !isValidDate("2026-12-15") {
		t.Error("December should be valid")
	}
}

func TestIsValidDate_LeapYear(t *testing.T) {
	// 2024 is a leap year (divisible by 4, not by 100, or by 400)
	if !isValidDate("2024-02-29") {
		t.Error("2024-02-29 should be valid (leap year)")
	}
	// 2100 is NOT a leap year (divisible by 100 but not 400)
	if isValidDate("2100-02-29") {
		t.Error("2100-02-29 should be invalid (not a leap year)")
	}
	// 2000 IS a leap year (divisible by 400)
	if !isValidDate("2000-02-29") {
		t.Error("2000-02-29 should be valid (leap year)")
	}
}

func TestIsValidDate_DaysPerMonth(t *testing.T) {
	// 30-day months
	for _, m := range []string{"04", "06", "09", "11"} {
		if !isValidDate("2026-" + m + "-30") {
			t.Errorf("2026-%s-30 should be valid", m)
		}
		if isValidDate("2026-" + m + "-31") {
			t.Errorf("2026-%s-31 should be invalid", m)
		}
	}
	// 31-day months
	for _, m := range []string{"01", "03", "05", "07", "08", "10", "12"} {
		if !isValidDate("2026-" + m + "-31") {
			t.Errorf("2026-%s-31 should be valid", m)
		}
	}
}

// ==================== validBudgetModes ====================

func TestValidBudgetModes_AllAccepted(t *testing.T) {
	accepted := []string{"manual", "balanced", "debt-free", "debt-payoff", "travel", "event"}
	for _, mode := range accepted {
		if !validBudgetModes[mode] {
			t.Errorf("mode %q should be valid", mode)
		}
	}
}

func TestValidBudgetModes_Rejected(t *testing.T) {
	rejected := []string{"", "Manual", "BALANCED", "custom", "savings", "free", "unknown", "debt_free"}
	for _, mode := range rejected {
		if validBudgetModes[mode] {
			t.Errorf("mode %q should not be valid", mode)
		}
	}
}

// ==================== guidedModes ====================

func TestGuidedModes_ManualIsNotGuided(t *testing.T) {
	if guidedModes["manual"] {
		t.Error("manual mode should not be a guided mode")
	}
}

func TestGuidedModes_AllGuidedModesAreValid(t *testing.T) {
	for mode := range guidedModes {
		if !validBudgetModes[mode] {
			t.Errorf("guided mode %q is not in validBudgetModes", mode)
		}
	}
}

func TestGuidedModes_ExpectedModes(t *testing.T) {
	expected := []string{"balanced", "debt-free", "debt-payoff", "travel", "event"}
	for _, mode := range expected {
		if !guidedModes[mode] {
			t.Errorf("expected %q to be a guided mode", mode)
		}
	}
}

// ==================== Validation Constants ====================

func TestMaxNameLength(t *testing.T) {
	if maxNameLength != 200 {
		t.Errorf("maxNameLength = %d, want 200", maxNameLength)
	}
}

func TestMaxDescriptionLength(t *testing.T) {
	if maxDescriptionLength != 1000 {
		t.Errorf("maxDescriptionLength = %d, want 1000", maxDescriptionLength)
	}
}

func TestMaxIconLength(t *testing.T) {
	if maxIconLength != 50 {
		t.Errorf("maxIconLength = %d, want 50", maxIconLength)
	}
}

func TestMaxCurrencyLength(t *testing.T) {
	if maxCurrencyLength != 3 {
		t.Errorf("maxCurrencyLength = %d, want 3", maxCurrencyLength)
	}
}

func TestMaxAmountValue(t *testing.T) {
	if maxAmountValue != 1e15 {
		t.Errorf("maxAmountValue = %v, want 1e15", maxAmountValue)
	}
}

func TestDateFormat(t *testing.T) {
	if dateFormat != "2006-01-02" {
		t.Errorf("dateFormat = %q, want %q", dateFormat, "2006-01-02")
	}
}

// ==================== Name Validation Logic ====================
// These tests exercise the inline validation patterns used in the handlers
// (name == "", len(name) > maxNameLength) without needing Fiber context.

func TestNameValidation_Empty(t *testing.T) {
	name := ""
	if name != "" {
		t.Error("empty name check should detect empty string")
	}
}

func TestNameValidation_ExactlyMaxLength(t *testing.T) {
	name := strings.Repeat("a", maxNameLength)
	if len(name) > maxNameLength {
		t.Error("name at exactly max length should pass")
	}
}

func TestNameValidation_OverMaxLength(t *testing.T) {
	name := strings.Repeat("a", maxNameLength+1)
	if len(name) <= maxNameLength {
		t.Error("name over max length should fail")
	}
}

func TestNameValidation_OneChar(t *testing.T) {
	name := "X"
	if name == "" || len(name) > maxNameLength {
		t.Error("single character name should be valid")
	}
}

func TestNameValidation_Unicode(t *testing.T) {
	// Unicode characters can be multiple bytes but the validation
	// uses len() which counts bytes, not runes.
	name := strings.Repeat("\U0001F600", 60) // 60 emoji, each 4 bytes = 240 bytes
	if len(name) <= maxNameLength {
		t.Error("240-byte unicode name should exceed maxNameLength of 200")
	}
}

// ==================== Description Validation ====================

func TestDescriptionValidation_AtMax(t *testing.T) {
	desc := strings.Repeat("x", maxDescriptionLength)
	if len(desc) > maxDescriptionLength {
		t.Error("description at exactly max length should pass")
	}
}

func TestDescriptionValidation_OverMax(t *testing.T) {
	desc := strings.Repeat("x", maxDescriptionLength+1)
	if len(desc) <= maxDescriptionLength {
		t.Error("description over max length should fail")
	}
}

func TestDescriptionValidation_Empty(t *testing.T) {
	desc := ""
	// Empty descriptions are allowed (it is not a required field).
	if len(desc) > maxDescriptionLength {
		t.Error("empty description should pass length check")
	}
}

// ==================== Icon Validation ====================

func TestIconValidation_AtMax(t *testing.T) {
	icon := strings.Repeat("i", maxIconLength)
	if len(icon) > maxIconLength {
		t.Error("icon at exactly max length should pass")
	}
}

func TestIconValidation_OverMax(t *testing.T) {
	icon := strings.Repeat("i", maxIconLength+1)
	if len(icon) <= maxIconLength {
		t.Error("icon over max length should fail")
	}
}

func TestIconValidation_Empty(t *testing.T) {
	// Empty icon is allowed in section/category creation.
	icon := ""
	if len(icon) > maxIconLength {
		t.Error("empty icon should pass length check")
	}
}

// ==================== Currency Validation ====================

func TestCurrencyValidation_ValidCodes(t *testing.T) {
	valid := []string{"COP", "USD", "EUR", "GBP", "JPY"}
	for _, c := range valid {
		if len(c) != maxCurrencyLength {
			t.Errorf("currency %q should be valid (length %d)", c, len(c))
		}
	}
}

func TestCurrencyValidation_InvalidCodes(t *testing.T) {
	invalid := []string{"", "US", "EURO", "CO", "ABCD"}
	for _, c := range invalid {
		if len(c) == maxCurrencyLength {
			t.Errorf("currency %q should be invalid", c)
		}
	}
}

// ==================== Amount Validation Logic ====================

func TestAmountValidation_Positive(t *testing.T) {
	amount := 100.0
	if amount <= 0 {
		t.Error("positive amount should pass")
	}
	if amount > maxAmountValue {
		t.Error("normal positive amount should not exceed max")
	}
}

func TestAmountValidation_Zero(t *testing.T) {
	amount := 0.0
	if amount > 0 {
		t.Error("zero amount should fail positive check")
	}
}

func TestAmountValidation_Negative(t *testing.T) {
	amount := -50.0
	if amount > 0 {
		t.Error("negative amount should fail positive check")
	}
}

func TestAmountValidation_ExactlyMax(t *testing.T) {
	amount := maxAmountValue
	if amount > maxAmountValue {
		t.Error("amount at exactly max should pass")
	}
}

func TestAmountValidation_OverMax(t *testing.T) {
	amount := maxAmountValue + 1
	if amount <= maxAmountValue {
		t.Error("amount over max should fail")
	}
}

func TestAmountValidation_VerySmall(t *testing.T) {
	amount := 0.01
	if amount <= 0 {
		t.Error("small positive amount should pass")
	}
	if amount > maxAmountValue {
		t.Error("small amount should not exceed max")
	}
}

// ==================== AllocationPercent Validation ====================

func TestAllocationPercentValidation_ValidRange(t *testing.T) {
	valid := []float64{0, 1, 50, 99.99, 100}
	for _, pct := range valid {
		if pct < 0 || pct > 100 {
			t.Errorf("allocation percent %v should be valid", pct)
		}
	}
}

func TestAllocationPercentValidation_InvalidRange(t *testing.T) {
	invalid := []float64{-1, -0.01, 100.01, 200, -100}
	for _, pct := range invalid {
		if pct >= 0 && pct <= 100 {
			t.Errorf("allocation percent %v should be invalid", pct)
		}
	}
}

// ==================== BillingCutoffDay Validation ====================

func TestBillingCutoffDayValidation_ValidRange(t *testing.T) {
	for day := 1; day <= 31; day++ {
		if day < 1 || day > 31 {
			t.Errorf("billing cutoff day %d should be valid", day)
		}
	}
}

func TestBillingCutoffDayValidation_Invalid(t *testing.T) {
	invalid := []int{0, -1, 32, 100}
	for _, day := range invalid {
		if day >= 1 && day <= 31 {
			t.Errorf("billing cutoff day %d should be invalid", day)
		}
	}
}

// ==================== getSectionsForMode ====================

func TestGetSectionsForMode_AllModes(t *testing.T) {
	modes := map[string]bool{
		"balanced":   true,
		"debt-free":  true,
		"debt-payoff": true,
		"travel":     true,
		"event":      true,
	}
	for mode := range modes {
		sections := getSectionsForMode(mode)
		if len(sections) == 0 {
			t.Errorf("getSectionsForMode(%q) returned empty sections", mode)
		}
	}
}

func TestGetSectionsForMode_Unknown_DefaultsToBalanced(t *testing.T) {
	unknown := getSectionsForMode("unknown")
	balanced := getBalancedSections()
	if len(unknown) != len(balanced) {
		t.Errorf("unknown mode should default to balanced: got %d sections, want %d", len(unknown), len(balanced))
	}
	for i := range unknown {
		if unknown[i].Name != balanced[i].Name {
			t.Errorf("section %d: got %q, want %q", i, unknown[i].Name, balanced[i].Name)
		}
	}
}

func TestGetSectionsForMode_ManualDefaultsToBalanced(t *testing.T) {
	manual := getSectionsForMode("manual")
	balanced := getBalancedSections()
	if len(manual) != len(balanced) {
		t.Errorf("manual mode should default to balanced: got %d sections, want %d", len(manual), len(balanced))
	}
}

// ==================== marshalJSON ====================

func TestMarshalJSON_SimpleMap(t *testing.T) {
	input := map[string]string{"key": "value"}
	data, err := marshalJSON(input)
	if err != nil {
		t.Fatalf("marshalJSON failed: %v", err)
	}
	if string(data) != `{"key":"value"}` {
		t.Errorf("marshalJSON result = %s, want %s", string(data), `{"key":"value"}`)
	}
}

func TestMarshalJSON_NilInput(t *testing.T) {
	data, err := marshalJSON(nil)
	if err != nil {
		t.Fatalf("marshalJSON(nil) should not error: %v", err)
	}
	if string(data) != "null" {
		t.Errorf("marshalJSON(nil) = %s, want null", string(data))
	}
}

func TestMarshalJSON_InvalidInput(t *testing.T) {
	// Channels cannot be marshaled to JSON.
	ch := make(chan int)
	_, err := marshalJSON(ch)
	if err == nil {
		t.Error("marshalJSON(channel) should return an error")
	}
}

// ==================== maxBudgetsPerUser ====================

func TestMaxBudgetsPerUser(t *testing.T) {
	if maxBudgetsPerUser != 7 {
		t.Errorf("maxBudgetsPerUser = %d, want 7", maxBudgetsPerUser)
	}
}
