package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ==================== Expense Unmarshal Edge Cases ====================

func TestExpenseUnmarshal_BothSubcategoryAndCategoryID_CategoryIDWins(t *testing.T) {
	// When both category_id and subcategory_id are present, category_id
	// takes precedence because the custom UnmarshalJSON only falls back
	// to subcategory_id when category_id is uuid.Nil.
	catID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	subID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"category_id": "` + catID + `",
		"subcategory_id": "` + subID + `",
		"amount": 100,
		"description": "test",
		"expense_date": "2026-01-01"
	}`

	var exp Expense
	if err := json.Unmarshal([]byte(raw), &exp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if exp.CategoryID.String() != catID {
		t.Errorf("CategoryID = %s, want %s (category_id should win)", exp.CategoryID, catID)
	}
}

func TestExpenseUnmarshal_NullSubcategoryID(t *testing.T) {
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"subcategory_id": null,
		"amount": 50,
		"description": "null test",
		"expense_date": "2026-01-01"
	}`

	var exp Expense
	if err := json.Unmarshal([]byte(raw), &exp); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// subcategory_id: null should leave CategoryID as uuid.Nil.
	if exp.CategoryID != uuid.Nil {
		t.Errorf("CategoryID = %s, want uuid.Nil for null subcategory_id", exp.CategoryID)
	}
}

func TestExpenseUnmarshal_EmptyStringUUID(t *testing.T) {
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"subcategory_id": "",
		"amount": 50,
		"description": "empty uuid",
		"expense_date": "2026-01-01"
	}`

	var exp Expense
	err := json.Unmarshal([]byte(raw), &exp)
	// An empty string is not a valid UUID, so unmarshal should error.
	if err == nil {
		t.Error("expected error when subcategory_id is empty string, got nil")
	}
}

func TestExpenseUnmarshal_MalformedUUID(t *testing.T) {
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"subcategory_id": "not-a-uuid-at-all",
		"amount": 50,
		"description": "bad uuid",
		"expense_date": "2026-01-01"
	}`

	var exp Expense
	err := json.Unmarshal([]byte(raw), &exp)
	if err == nil {
		t.Error("expected error for malformed UUID in subcategory_id, got nil")
	}
}

func TestExpenseUnmarshal_ExtraUnknownFields(t *testing.T) {
	// Extra fields should be silently ignored by encoding/json.
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"category_id": "11111111-1111-1111-1111-111111111111",
		"amount": 50,
		"description": "extra fields",
		"expense_date": "2026-01-01",
		"unknown_field": "should be ignored",
		"another_extra": 42,
		"nested_extra": {"foo": "bar"}
	}`

	var exp Expense
	if err := json.Unmarshal([]byte(raw), &exp); err != nil {
		t.Fatalf("unmarshal should succeed with extra fields, got: %v", err)
	}
	if exp.Amount != 50 {
		t.Errorf("Amount = %v, want 50", exp.Amount)
	}
	if exp.CategoryID.String() != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("CategoryID wrong: got %s", exp.CategoryID)
	}
}

func TestExpenseUnmarshal_MissingRequiredFields(t *testing.T) {
	// encoding/json does not enforce required fields; missing fields get
	// zero values. This test documents that behavior.
	raw := `{}`

	var exp Expense
	if err := json.Unmarshal([]byte(raw), &exp); err != nil {
		t.Fatalf("unmarshal empty object should not error: %v", err)
	}

	if exp.ID != uuid.Nil {
		t.Errorf("ID should be uuid.Nil for empty JSON, got %s", exp.ID)
	}
	if exp.Amount != 0 {
		t.Errorf("Amount should be 0 for empty JSON, got %v", exp.Amount)
	}
	if exp.Description != "" {
		t.Errorf("Description should be empty for empty JSON, got %q", exp.Description)
	}
	if exp.ExpenseDate != "" {
		t.Errorf("ExpenseDate should be empty for empty JSON, got %q", exp.ExpenseDate)
	}
}

func TestExpenseUnmarshal_WrongTypes(t *testing.T) {
	// Amount is a string instead of a number -- should produce an error.
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"amount": "not-a-number",
		"description": "wrong type",
		"expense_date": "2026-01-01"
	}`

	var exp Expense
	err := json.Unmarshal([]byte(raw), &exp)
	if err == nil {
		t.Error("expected error when amount is a string, got nil")
	}
}

func TestExpenseUnmarshal_AmountAsString(t *testing.T) {
	// Integer where float is expected should work fine.
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"amount": 100,
		"description": "integer amount",
		"expense_date": "2026-01-01"
	}`

	var exp Expense
	if err := json.Unmarshal([]byte(raw), &exp); err != nil {
		t.Fatalf("integer amount should unmarshal fine: %v", err)
	}
	if exp.Amount != 100 {
		t.Errorf("Amount = %v, want 100", exp.Amount)
	}
}

// ==================== Expense Marshal/Unmarshal Round-Trip ====================

func TestExpense_RoundTrip_PreservesAllFields(t *testing.T) {
	createdBy := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	original := Expense{
		ID:          uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		BudgetID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		CategoryID:  uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Amount:      12345.67,
		Description: "Round-trip test with special chars: <>&\"'",
		ExpenseDate: "2026-04-10",
		CreatedBy:   &createdBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded Expense
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID: got %s, want %s", decoded.ID, original.ID)
	}
	if decoded.BudgetID != original.BudgetID {
		t.Errorf("BudgetID: got %s, want %s", decoded.BudgetID, original.BudgetID)
	}
	if decoded.CategoryID != original.CategoryID {
		t.Errorf("CategoryID: got %s, want %s", decoded.CategoryID, original.CategoryID)
	}
	if decoded.Amount != original.Amount {
		t.Errorf("Amount: got %v, want %v", decoded.Amount, original.Amount)
	}
	if decoded.Description != original.Description {
		t.Errorf("Description: got %q, want %q", decoded.Description, original.Description)
	}
	if decoded.ExpenseDate != original.ExpenseDate {
		t.Errorf("ExpenseDate: got %q, want %q", decoded.ExpenseDate, original.ExpenseDate)
	}
	if decoded.CreatedBy == nil || *decoded.CreatedBy != *original.CreatedBy {
		t.Errorf("CreatedBy: got %v, want %v", decoded.CreatedBy, original.CreatedBy)
	}
	if !decoded.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", decoded.CreatedAt, original.CreatedAt)
	}
	if !decoded.UpdatedAt.Equal(original.UpdatedAt) {
		t.Errorf("UpdatedAt: got %v, want %v", decoded.UpdatedAt, original.UpdatedAt)
	}
}

// ==================== CreateExpenseRequest Edge Cases ====================

func TestCreateExpenseRequest_ZeroUUID(t *testing.T) {
	raw := `{
		"category_id": "00000000-0000-0000-0000-000000000000",
		"amount": 100,
		"description": "zero uuid",
		"expense_date": "2026-01-01"
	}`

	var req CreateExpenseRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if req.CategoryID != uuid.Nil {
		t.Errorf("CategoryID should be uuid.Nil for zero UUID, got %s", req.CategoryID)
	}
}

// ==================== UpdateExpenseRequest Edge Cases ====================

func TestUpdateExpenseRequest_AllNilFields(t *testing.T) {
	raw := `{}`

	var req UpdateExpenseRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if req.CategoryID != nil {
		t.Error("CategoryID should be nil")
	}
	if req.Amount != nil {
		t.Error("Amount should be nil")
	}
	if req.Description != nil {
		t.Error("Description should be nil")
	}
	if req.ExpenseDate != nil {
		t.Error("ExpenseDate should be nil")
	}
}

func TestUpdateExpenseRequest_NullValues(t *testing.T) {
	// JSON null should leave pointer fields as nil.
	raw := `{
		"category_id": null,
		"amount": null,
		"description": null,
		"expense_date": null
	}`

	var req UpdateExpenseRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if req.CategoryID != nil {
		t.Error("CategoryID should be nil for JSON null")
	}
	if req.Amount != nil {
		t.Error("Amount should be nil for JSON null")
	}
	if req.Description != nil {
		t.Error("Description should be nil for JSON null")
	}
	if req.ExpenseDate != nil {
		t.Error("ExpenseDate should be nil for JSON null")
	}
}

// ==================== Large Description Field ====================

func TestExpense_LargeDescription(t *testing.T) {
	// 10KB description string.
	largeDesc := strings.Repeat("A", 10*1024)

	exp := Expense{
		ID:          uuid.New(),
		BudgetID:    uuid.New(),
		CategoryID:  uuid.New(),
		Amount:      100,
		Description: largeDesc,
		ExpenseDate: "2026-01-01",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatalf("marshal with large description failed: %v", err)
	}

	var decoded Expense
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal with large description failed: %v", err)
	}

	if len(decoded.Description) != 10*1024 {
		t.Errorf("Description length = %d, want %d", len(decoded.Description), 10*1024)
	}
}

// ==================== Category with Zero-Value CategoryID ====================

func TestCategory_ZeroValueCategoryID(t *testing.T) {
	cat := Category{
		ID:                uuid.New(),
		CategoryID:        uuid.Nil, // zero-value parent reference
		Name:              "Orphan category",
		AllocationPercent: 50,
		Icon:              "home",
		SortOrder:         1,
		CreatedAt:         time.Now(),
	}

	data, err := json.Marshal(cat)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map failed: %v", err)
	}

	// The category_id should be the zero UUID string, not omitted.
	catIDStr, ok := m["category_id"].(string)
	if !ok {
		t.Fatal("category_id should be present as a string in JSON")
	}
	if catIDStr != uuid.Nil.String() {
		t.Errorf("category_id = %q, want %q", catIDStr, uuid.Nil.String())
	}
}

// ==================== SummaryCategoryView section_id Mapping ====================

func TestSummaryCategoryView_NilUUID_SectionID(t *testing.T) {
	scv := SummaryCategoryView{
		ID:                uuid.New(),
		SectionID:         uuid.Nil,
		Name:              "Test",
		AllocationPercent: 50,
		Icon:              "home",
		SortOrder:         1,
		CreatedAt:         time.Now(),
	}

	data, err := json.Marshal(scv)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal to map failed: %v", err)
	}

	// section_id should be present even when it is the zero UUID.
	sectionIDStr, ok := m["section_id"].(string)
	if !ok {
		t.Fatal("section_id should be present")
	}
	if sectionIDStr != uuid.Nil.String() {
		t.Errorf("section_id = %q, want %q", sectionIDStr, uuid.Nil.String())
	}
}

// ==================== Budget Edge Cases ====================

func TestBudget_NegativeMonthlyIncome(t *testing.T) {
	budget := Budget{
		ID:                  uuid.New(),
		UserID:              uuid.New(),
		Name:                "Negative Income",
		MonthlyIncome:       -5000,
		Currency:            "USD",
		BillingPeriodMonths: 1,
		BillingCutoffDay:    1,
		Mode:                "manual",
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	data, err := json.Marshal(budget)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded Budget
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.MonthlyIncome != -5000 {
		t.Errorf("MonthlyIncome = %v, want -5000", decoded.MonthlyIncome)
	}
}

func TestBudget_ZeroBillingPeriodMonths(t *testing.T) {
	budget := Budget{
		ID:                  uuid.New(),
		UserID:              uuid.New(),
		Name:                "One-time Budget",
		MonthlyIncome:       10000,
		Currency:            "USD",
		BillingPeriodMonths: 0,
		BillingCutoffDay:    1,
		Mode:                "manual",
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	data, err := json.Marshal(budget)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded Budget
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.BillingPeriodMonths != 0 {
		t.Errorf("BillingPeriodMonths = %d, want 0", decoded.BillingPeriodMonths)
	}
}

// ==================== Expense with Extreme Amounts ====================

func TestExpense_VeryLargeAmount(t *testing.T) {
	exp := Expense{
		ID:          uuid.New(),
		BudgetID:    uuid.New(),
		CategoryID:  uuid.New(),
		Amount:      1e15,
		Description: "max amount",
		ExpenseDate: "2026-01-01",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded Expense
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.Amount != 1e15 {
		t.Errorf("Amount = %v, want 1e15", decoded.Amount)
	}
}

func TestExpense_VerySmallAmount(t *testing.T) {
	exp := Expense{
		ID:          uuid.New(),
		BudgetID:    uuid.New(),
		CategoryID:  uuid.New(),
		Amount:      0.01,
		Description: "minimum meaningful amount",
		ExpenseDate: "2026-01-01",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded Expense
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.Amount != 0.01 {
		t.Errorf("Amount = %v, want 0.01", decoded.Amount)
	}
}

// ==================== ErrorResponse Edge Cases ====================

func TestErrorResponse_EmptyMessage(t *testing.T) {
	errResp := ErrorResponse{Error: ""}

	data, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	if string(data) != `{"error":""}` {
		t.Errorf("got %s, want %s", string(data), `{"error":""}`)
	}
}

func TestErrorResponse_LongMessage(t *testing.T) {
	longMsg := strings.Repeat("x", 5000)
	errResp := ErrorResponse{Error: longMsg}

	data, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded ErrorResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if decoded.Error != longMsg {
		t.Errorf("Error message length = %d, want %d", len(decoded.Error), len(longMsg))
	}
}

// ==================== Expense Unmarshal with boolean for amount ====================

func TestExpenseUnmarshal_BooleanAmount(t *testing.T) {
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"amount": true,
		"description": "bool amount",
		"expense_date": "2026-01-01"
	}`

	var exp Expense
	err := json.Unmarshal([]byte(raw), &exp)
	if err == nil {
		t.Error("expected error when amount is boolean, got nil")
	}
}

// ==================== Expense Unmarshal with array for amount ====================

func TestExpenseUnmarshal_ArrayAmount(t *testing.T) {
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"amount": [1, 2, 3],
		"description": "array amount",
		"expense_date": "2026-01-01"
	}`

	var exp Expense
	err := json.Unmarshal([]byte(raw), &exp)
	if err == nil {
		t.Error("expected error when amount is an array, got nil")
	}
}

// ==================== Multiple expenses unmarshal with mixed fields ====================

func TestExpenseUnmarshal_ArrayMixedCategoryFields(t *testing.T) {
	// Array of expenses: one uses category_id, one uses subcategory_id.
	raw := `[
		{
			"id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			"budget_id": "22222222-2222-2222-2222-222222222222",
			"category_id": "11111111-1111-1111-1111-111111111111",
			"amount": 100,
			"description": "first",
			"expense_date": "2026-01-01"
		},
		{
			"id": "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
			"budget_id": "22222222-2222-2222-2222-222222222222",
			"subcategory_id": "99999999-9999-9999-9999-999999999999",
			"amount": 200,
			"description": "second",
			"expense_date": "2026-01-02"
		}
	]`

	var expenses []Expense
	if err := json.Unmarshal([]byte(raw), &expenses); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if len(expenses) != 2 {
		t.Fatalf("expected 2 expenses, got %d", len(expenses))
	}

	if expenses[0].CategoryID.String() != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("first expense CategoryID = %s, want 11111111-...", expenses[0].CategoryID)
	}
	if expenses[1].CategoryID.String() != "99999999-9999-9999-9999-999999999999" {
		t.Errorf("second expense CategoryID = %s, want 99999999-...", expenses[1].CategoryID)
	}
}
