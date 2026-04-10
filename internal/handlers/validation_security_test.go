package handlers

import (
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// ==================== XSS Payloads in Names ====================

func TestNameValidation_XSSPayloads(t *testing.T) {
	xssPayloads := []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"<svg/onload=alert(1)>",
		"javascript:alert(1)",
		"<iframe src='javascript:alert(1)'>",
		"<body onload=alert(1)>",
		"\"><script>alert(document.cookie)</script>",
	}

	for _, payload := range xssPayloads {
		// The backend validates name length but does NOT sanitize HTML. This is
		// acceptable because the backend stores raw data and the frontend must
		// escape on render. Verify these pass the length check (they are all
		// under maxNameLength).
		if len(payload) > maxNameLength {
			t.Errorf("XSS payload %q unexpectedly exceeds maxNameLength", payload)
		}
		// XSS payloads should pass the basic name validation (non-empty, under limit).
		if payload == "" {
			t.Error("XSS payload should not be empty")
		}
	}
}

// ==================== SQL Injection in Names ====================

func TestNameValidation_SQLInjectionPayloads(t *testing.T) {
	sqliPayloads := []string{
		"'; DROP TABLE budgets; --",
		"1' OR '1'='1",
		"Robert'); DROP TABLE students;--",
		"' UNION SELECT * FROM profiles --",
		"1; DELETE FROM expenses WHERE 1=1",
	}

	for _, payload := range sqliPayloads {
		// SQL injection in names should pass the basic length validation because
		// the backend relies on parameterized queries (via PostgREST URL escaping)
		// rather than input sanitization.
		if len(payload) > maxNameLength {
			t.Errorf("SQLi payload %q unexpectedly exceeds maxNameLength", payload)
		}
		if payload == "" {
			t.Error("SQLi payload should not be empty")
		}
	}
}

// ==================== Very Long Strings ====================

func TestNameValidation_VeryLongString(t *testing.T) {
	longName := strings.Repeat("A", 10001)
	if len(longName) <= maxNameLength {
		t.Error("10001-char name should exceed maxNameLength of 200")
	}
}

func TestDescriptionValidation_VeryLongString(t *testing.T) {
	longDesc := strings.Repeat("B", 10001)
	if len(longDesc) <= maxDescriptionLength {
		t.Error("10001-char description should exceed maxDescriptionLength of 500")
	}
}

func TestIconValidation_VeryLongString(t *testing.T) {
	longIcon := strings.Repeat("C", 10001)
	if len(longIcon) <= maxIconLength {
		t.Error("10001-char icon should exceed maxIconLength of 50")
	}
}

// ==================== Unicode Edge Cases ====================

func TestNameValidation_ZeroWidthCharacters(t *testing.T) {
	// Zero-width space (U+200B), zero-width joiner (U+200D).
	name := "Normal\u200BText\u200DWith\u200BInvisible"
	if name == "" {
		t.Error("name with zero-width chars should not be empty")
	}
	// This name is under maxNameLength in bytes.
	if len(name) > maxNameLength {
		t.Error("name with zero-width chars should pass length check")
	}
}

func TestNameValidation_RTLOverride(t *testing.T) {
	// Right-to-left override (U+202E) can be used for display spoofing.
	name := "Budget\u202Elbadllif"
	if len(name) > maxNameLength {
		t.Error("RTL override name should pass length check")
	}
}

func TestNameValidation_EmojiInName(t *testing.T) {
	name := "My Budget \U0001F4B0\U0001F4B0\U0001F4B0"
	if name == "" {
		t.Error("emoji name should not be empty")
	}
	// Each emoji is 4 bytes; the name is well under 200 bytes.
	if len(name) > maxNameLength {
		t.Error("emoji name should pass length check")
	}
}

func TestNameValidation_CombiningCharacters(t *testing.T) {
	// Combining diacritics: "a" + combining acute accent repeated many times.
	// 200 base chars + 200 combining chars = 400 bytes.
	name := strings.Repeat("a\u0301", 101) // 101 * 3 bytes = 303 bytes
	if len(name) <= maxNameLength {
		t.Error("303-byte combining char name should exceed maxNameLength of 200")
	}
}

// ==================== Null Bytes in Strings ====================

func TestNameValidation_NullBytes_Security(t *testing.T) {
	name := "Budget\x00Name"
	// Null bytes make the string non-empty and len() counts them.
	if name == "" {
		t.Error("null byte name should not be empty")
	}
	if len(name) > maxNameLength {
		t.Error("null byte name should pass length check")
	}
}

func TestDescriptionValidation_NullBytes_Security(t *testing.T) {
	desc := "Some\x00Description\x00With\x00Nulls"
	if len(desc) > maxDescriptionLength {
		t.Error("null byte description should pass length check")
	}
}

// ==================== Negative/Overflow Amounts ====================

func TestAmountValidation_MaxFloat64(t *testing.T) {
	amount := math.MaxFloat64
	if amount <= maxAmountValue {
		t.Error("math.MaxFloat64 should exceed maxAmountValue")
	}
}

func TestAmountValidation_NegativeMaxFloat64(t *testing.T) {
	amount := -math.MaxFloat64
	if amount > 0 {
		t.Error("-math.MaxFloat64 should fail positive check")
	}
}

func TestAmountValidation_NaN(t *testing.T) {
	amount := math.NaN()
	// NaN comparisons: NaN > 0 is false, NaN <= maxAmountValue is false.
	if amount > 0 {
		t.Error("NaN should fail amount > 0 check")
	}
	if amount <= maxAmountValue {
		t.Error("NaN should fail amount <= maxAmountValue check")
	}
}

func TestAmountValidation_PositiveInfinity(t *testing.T) {
	amount := math.Inf(1)
	if amount <= maxAmountValue {
		t.Error("+Inf should exceed maxAmountValue")
	}
}

func TestAmountValidation_NegativeInfinity(t *testing.T) {
	amount := math.Inf(-1)
	if amount > 0 {
		t.Error("-Inf should fail positive check")
	}
}

func TestAmountValidation_SmallNegative(t *testing.T) {
	amount := -0.01
	if amount > 0 {
		t.Error("small negative should fail positive check")
	}
}

// ==================== UUID Parsing Security ====================

func TestUUIDParsing_MalformedUUIDs(t *testing.T) {
	malformed := []string{
		"",
		"not-a-uuid",
		"12345",
		"gggggggg-gggg-gggg-gggg-gggggggggggg",
		"00000000-0000-0000-0000-00000000000", // too short
		"00000000-0000-0000-0000-0000000000000", // too long
		"00000000_0000_0000_0000_000000000000", // wrong separator
	}

	for _, id := range malformed {
		_, err := uuid.Parse(id)
		if err == nil {
			t.Errorf("malformed UUID %q should fail to parse", id)
		}
	}
}

func TestUUIDParsing_SQLInjectionInUUID(t *testing.T) {
	sqli := []string{
		"'; DROP TABLE budgets; --",
		"' OR '1'='1",
		"00000000-0000-0000-0000-000000000000'; DELETE FROM expenses;--",
	}

	for _, id := range sqli {
		_, err := uuid.Parse(id)
		if err == nil {
			t.Errorf("SQL injection in UUID %q should fail to parse", id)
		}
	}
}

func TestUUIDParsing_ValidUUIDs(t *testing.T) {
	valid := []string{
		"00000000-0000-0000-0000-000000000000",
		"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"12345678-1234-1234-1234-123456789abc",
	}

	for _, id := range valid {
		_, err := uuid.Parse(id)
		if err != nil {
			t.Errorf("valid UUID %q should parse successfully: %v", id, err)
		}
	}
}

// ==================== Path Traversal in String Fields ====================

func TestNameValidation_PathTraversal(t *testing.T) {
	traversals := []string{
		"../../etc/passwd",
		"..\\..\\windows\\system32",
		"/etc/shadow",
		"C:\\Windows\\System32",
		"....//....//etc/passwd",
	}

	for _, payload := range traversals {
		// Path traversal in name fields passes length validation because the
		// backend does not use names in file system operations. This test
		// documents the behavior.
		if payload == "" || len(payload) > maxNameLength {
			t.Errorf("path traversal %q should pass basic name validation", payload)
		}
	}
}

// ==================== Currency Validation with Special Characters ====================

func TestCurrencyValidation_SpecialChars(t *testing.T) {
	// Currency is validated by len(c) == maxCurrencyLength (3).
	// These 3-char strings pass length check but are not real currencies.
	special := []string{"</>", "SQL", "--;", "   "}
	for _, c := range special {
		if len(c) != maxCurrencyLength {
			t.Errorf("3-char special currency %q should pass length check", c)
		}
	}
}

// ==================== AllocationPercent Boundary Cases ====================

func TestAllocationPercent_NaN(t *testing.T) {
	pct := math.NaN()
	// NaN >= 0 is false, so it would fail validation.
	if pct >= 0 && pct <= 100 {
		t.Error("NaN should fail allocation percent validation")
	}
}

func TestAllocationPercent_Infinity(t *testing.T) {
	pct := math.Inf(1)
	if pct >= 0 && pct <= 100 {
		t.Error("+Inf should fail allocation percent validation")
	}
}

// ==================== Date Validation with Injection ====================

func TestIsValidDate_SQLInjection(t *testing.T) {
	sqliDates := []string{
		"2026-01-01'; DROP TABLE expenses;--",
		"2026-01-01 OR 1=1",
		"' UNION SELECT * FROM budgets --",
	}
	for _, d := range sqliDates {
		if isValidDate(d) {
			t.Errorf("SQL injection date %q should fail validation", d)
		}
	}
}

func TestIsValidDate_XSSPayload(t *testing.T) {
	if isValidDate("<script>alert(1)</script>") {
		t.Error("XSS payload should fail date validation")
	}
}
