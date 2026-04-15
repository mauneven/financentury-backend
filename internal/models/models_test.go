package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ==================== Expense JSON Serialization ====================

func TestExpense_MarshalJSON(t *testing.T) {
	catID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	budgetID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	expenseID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	createdBy := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	exp := Expense{
		ID:          expenseID,
		BudgetID:    budgetID,
		CategoryID:  catID,
		Amount:      150.50,
		Description: "Groceries",
		ExpenseDate: "2026-04-10",
		CreatedBy:   &createdBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatalf("failed to marshal Expense: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	// Verify the JSON key is "category_id" (not "subcategory_id").
	if _, ok := m["category_id"]; !ok {
		t.Error("serialized Expense should have key 'category_id'")
	}
	if _, ok := m["subcategory_id"]; ok {
		t.Error("serialized Expense should NOT have key 'subcategory_id'")
	}

	// Verify all expected keys are present.
	expectedKeys := []string{"id", "budget_id", "category_id", "amount", "description", "expense_date", "created_by", "created_at", "updated_at"}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("serialized Expense missing key %q", key)
		}
	}
}

func TestExpense_UnmarshalJSON_CategoryID(t *testing.T) {
	catID := "11111111-1111-1111-1111-111111111111"
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"category_id": "` + catID + `",
		"amount": 150.50,
		"description": "Groceries",
		"expense_date": "2026-04-10"
	}`

	var exp Expense
	if err := json.Unmarshal([]byte(raw), &exp); err != nil {
		t.Fatalf("failed to unmarshal Expense: %v", err)
	}

	if exp.CategoryID.String() != catID {
		t.Errorf("CategoryID = %s, want %s", exp.CategoryID, catID)
	}
}

func TestExpense_UnmarshalJSON_SubcategoryID_FallbackPopulatesCategoryID(t *testing.T) {
	// The Expense type has a custom UnmarshalJSON that falls back to
	// "subcategory_id" when "category_id" is absent. This compatibility
	// layer exists because the DB column is still named subcategory_id
	// even though the Go/API field is category_id.
	catID := "11111111-1111-1111-1111-111111111111"
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"subcategory_id": "` + catID + `",
		"amount": 150.50,
		"description": "Groceries",
		"expense_date": "2026-04-10"
	}`

	var exp Expense
	if err := json.Unmarshal([]byte(raw), &exp); err != nil {
		t.Fatalf("failed to unmarshal Expense: %v", err)
	}

	// CategoryID should be populated from subcategory_id via the fallback.
	if exp.CategoryID.String() != catID {
		t.Errorf("CategoryID should be populated from subcategory_id fallback, got %s, want %s", exp.CategoryID, catID)
	}
}

func TestExpense_UnmarshalJSON_CategoryID_TakesPrecedenceOverSubcategoryID(t *testing.T) {
	// When both category_id and subcategory_id are present, category_id wins.
	catID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	subID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"category_id": "` + catID + `",
		"subcategory_id": "` + subID + `",
		"amount": 150.50,
		"description": "Groceries",
		"expense_date": "2026-04-10"
	}`

	var exp Expense
	if err := json.Unmarshal([]byte(raw), &exp); err != nil {
		t.Fatalf("failed to unmarshal Expense: %v", err)
	}

	// category_id should take precedence; subcategory_id fallback should NOT overwrite.
	if exp.CategoryID.String() != catID {
		t.Errorf("CategoryID should be %s (from category_id), got %s", catID, exp.CategoryID)
	}
}

func TestExpense_UnmarshalJSON_NeitherCategoryField(t *testing.T) {
	// When neither category_id nor subcategory_id are present, CategoryID
	// should be uuid.Nil.
	raw := `{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"amount": 150.50,
		"description": "Groceries",
		"expense_date": "2026-04-10"
	}`

	var exp Expense
	if err := json.Unmarshal([]byte(raw), &exp); err != nil {
		t.Fatalf("failed to unmarshal Expense: %v", err)
	}

	if exp.CategoryID != uuid.Nil {
		t.Errorf("CategoryID should be uuid.Nil when neither field is present, got %s", exp.CategoryID)
	}
}

func TestExpense_CreatedByOmitempty(t *testing.T) {
	exp := Expense{
		ID:          uuid.New(),
		BudgetID:    uuid.New(),
		CategoryID:  uuid.New(),
		Amount:      10,
		Description: "test",
		ExpenseDate: "2026-01-01",
		CreatedBy:   nil,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	data, err := json.Marshal(exp)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// created_by should be omitted when nil.
	if _, ok := m["created_by"]; ok {
		t.Error("created_by should be omitted when nil")
	}
}

// ==================== CreateExpenseRequest JSON ====================

func TestCreateExpenseRequest_UnmarshalJSON(t *testing.T) {
	catID := "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	raw := `{
		"category_id": "` + catID + `",
		"amount": 250.99,
		"description": "Test expense",
		"expense_date": "2026-05-01"
	}`

	var req CreateExpenseRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal CreateExpenseRequest: %v", err)
	}

	if req.CategoryID.String() != catID {
		t.Errorf("CategoryID = %s, want %s", req.CategoryID, catID)
	}
	if req.Amount != 250.99 {
		t.Errorf("Amount = %v, want 250.99", req.Amount)
	}
	if req.Description != "Test expense" {
		t.Errorf("Description = %q, want %q", req.Description, "Test expense")
	}
	if req.ExpenseDate != "2026-05-01" {
		t.Errorf("ExpenseDate = %q, want %q", req.ExpenseDate, "2026-05-01")
	}
}

func TestCreateExpenseRequest_SubcategoryIDIgnored(t *testing.T) {
	// Using the old "subcategory_id" key should NOT populate CategoryID.
	raw := `{
		"subcategory_id": "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		"amount": 100,
		"description": "test",
		"expense_date": "2026-01-01"
	}`

	var req CreateExpenseRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.CategoryID != uuid.Nil {
		t.Errorf("CategoryID should be uuid.Nil when 'subcategory_id' is used, got %s", req.CategoryID)
	}
}

// ==================== UpdateExpenseRequest JSON ====================

func TestUpdateExpenseRequest_PartialJSON(t *testing.T) {
	// Only amount is provided.
	raw := `{"amount": 500}`

	var req UpdateExpenseRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.Amount == nil {
		t.Fatal("Amount should not be nil")
	}
	if *req.Amount != 500 {
		t.Errorf("Amount = %v, want 500", *req.Amount)
	}

	// Other fields should remain nil.
	if req.CategoryID != nil {
		t.Error("CategoryID should be nil when not provided")
	}
	if req.Description != nil {
		t.Error("Description should be nil when not provided")
	}
	if req.ExpenseDate != nil {
		t.Error("ExpenseDate should be nil when not provided")
	}
}

func TestUpdateExpenseRequest_AllFields(t *testing.T) {
	catID := "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	raw := `{
		"category_id": "` + catID + `",
		"amount": 300,
		"description": "Updated",
		"expense_date": "2026-06-15"
	}`

	var req UpdateExpenseRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.CategoryID == nil || req.CategoryID.String() != catID {
		t.Errorf("CategoryID = %v, want %s", req.CategoryID, catID)
	}
	if req.Amount == nil || *req.Amount != 300 {
		t.Errorf("Amount = %v, want 300", req.Amount)
	}
	if req.Description == nil || *req.Description != "Updated" {
		t.Errorf("Description = %v, want 'Updated'", req.Description)
	}
	if req.ExpenseDate == nil || *req.ExpenseDate != "2026-06-15" {
		t.Errorf("ExpenseDate = %v, want '2026-06-15'", req.ExpenseDate)
	}
}

func TestUpdateExpenseRequest_EmptyJSON(t *testing.T) {
	raw := `{}`

	var req UpdateExpenseRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.CategoryID != nil {
		t.Error("CategoryID should be nil for empty JSON")
	}
	if req.Amount != nil {
		t.Error("Amount should be nil for empty JSON")
	}
	if req.Description != nil {
		t.Error("Description should be nil for empty JSON")
	}
	if req.ExpenseDate != nil {
		t.Error("ExpenseDate should be nil for empty JSON")
	}
}

// ==================== Budget JSON ====================

func TestBudget_RoundTripJSON(t *testing.T) {
	budgetID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	userID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)

	original := Budget{
		ID:                  budgetID,
		UserID:              userID,
		Name:                "My Budget",
		MonthlyIncome:       5000000,
		Currency:            "COP",
		BillingPeriodMonths: 1,
		BillingCutoffDay:    1,
		Mode:                "balanced",
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal Budget: %v", err)
	}

	var decoded Budget
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal Budget: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID mismatch: got %s, want %s", decoded.ID, original.ID)
	}
	if decoded.UserID != original.UserID {
		t.Errorf("UserID mismatch: got %s, want %s", decoded.UserID, original.UserID)
	}
	if decoded.Name != original.Name {
		t.Errorf("Name mismatch: got %q, want %q", decoded.Name, original.Name)
	}
	if decoded.MonthlyIncome != original.MonthlyIncome {
		t.Errorf("MonthlyIncome mismatch: got %v, want %v", decoded.MonthlyIncome, original.MonthlyIncome)
	}
	if decoded.Currency != original.Currency {
		t.Errorf("Currency mismatch: got %q, want %q", decoded.Currency, original.Currency)
	}
	if decoded.BillingPeriodMonths != original.BillingPeriodMonths {
		t.Errorf("BillingPeriodMonths mismatch: got %d, want %d", decoded.BillingPeriodMonths, original.BillingPeriodMonths)
	}
	if decoded.BillingCutoffDay != original.BillingCutoffDay {
		t.Errorf("BillingCutoffDay mismatch: got %d, want %d", decoded.BillingCutoffDay, original.BillingCutoffDay)
	}
	if decoded.Mode != original.Mode {
		t.Errorf("Mode mismatch: got %q, want %q", decoded.Mode, original.Mode)
	}
}

// ==================== CreateBudgetRequest JSON ====================

func TestCreateBudgetRequest_UnmarshalJSON(t *testing.T) {
	raw := `{
		"name": "Test Budget",
		"monthly_income": 3000000,
		"currency": "COP",
		"billing_period_months": 1,
		"billing_cutoff_day": 15,
		"mode": "balanced"
	}`

	var req CreateBudgetRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.Name != "Test Budget" {
		t.Errorf("Name = %q, want %q", req.Name, "Test Budget")
	}
	if req.MonthlyIncome != 3000000 {
		t.Errorf("MonthlyIncome = %v, want 3000000", req.MonthlyIncome)
	}
	if req.Currency != "COP" {
		t.Errorf("Currency = %q, want %q", req.Currency, "COP")
	}
	if req.BillingPeriodMonths != 1 {
		t.Errorf("BillingPeriodMonths = %d, want 1", req.BillingPeriodMonths)
	}
	if req.BillingCutoffDay != 15 {
		t.Errorf("BillingCutoffDay = %d, want 15", req.BillingCutoffDay)
	}
	if req.Mode != "balanced" {
		t.Errorf("Mode = %q, want %q", req.Mode, "balanced")
	}
}

// ==================== UpdateBudgetRequest JSON ====================

func TestUpdateBudgetRequest_OmitEmpty(t *testing.T) {
	// Only name is provided.
	raw := `{"name": "New Name"}`

	var req UpdateBudgetRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if req.Name == nil || *req.Name != "New Name" {
		t.Errorf("Name = %v, want 'New Name'", req.Name)
	}
	if req.MonthlyIncome != nil {
		t.Error("MonthlyIncome should be nil when not provided")
	}
	if req.Currency != nil {
		t.Error("Currency should be nil when not provided")
	}
	if req.BillingPeriodMonths != nil {
		t.Error("BillingPeriodMonths should be nil when not provided")
	}
	if req.BillingCutoffDay != nil {
		t.Error("BillingCutoffDay should be nil when not provided")
	}
	if req.Mode != nil {
		t.Error("Mode should be nil when not provided")
	}
}

// ==================== Section JSON ====================

func TestSection_RoundTripJSON(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	sectionID := uuid.New()
	budgetID := uuid.New()

	original := Section{
		ID:                sectionID,
		BudgetID:          budgetID,
		Name:              "Necesidades",
		AllocationPercent: 50,
		Icon:              "home",
		SortOrder:         1,
		CreatedAt:         now,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("failed to marshal Section: %v", err)
	}

	var decoded Section
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal Section: %v", err)
	}

	if decoded.Name != original.Name {
		t.Errorf("Name = %q, want %q", decoded.Name, original.Name)
	}
	if decoded.AllocationPercent != original.AllocationPercent {
		t.Errorf("AllocationPercent = %v, want %v", decoded.AllocationPercent, original.AllocationPercent)
	}
	if decoded.Icon != original.Icon {
		t.Errorf("Icon = %q, want %q", decoded.Icon, original.Icon)
	}
}

// ==================== Category JSON ====================

func TestCategory_JSONKeys(t *testing.T) {
	catID := uuid.New()
	sectionID := uuid.New()
	now := time.Now()

	cat := Category{
		ID:                catID,
		CategoryID:        sectionID,
		Name:              "Vivienda",
		AllocationPercent: 56,
		Icon:              "home",
		SortOrder:         1,
		CreatedAt:         now,
	}

	data, err := json.Marshal(cat)
	if err != nil {
		t.Fatalf("failed to marshal Category: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	// The parent reference field should serialize as "category_id"
	if _, ok := m["category_id"]; !ok {
		t.Error("Category should have 'category_id' key in JSON")
	}
}

// ==================== SummaryCategoryView JSON ====================

func TestSummaryCategoryView_SectionIDMapping(t *testing.T) {
	// SummaryCategoryView maps "section_id" in JSON to the SectionID field.
	sectionID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

	scv := SummaryCategoryView{
		ID:                uuid.New(),
		SectionID:         sectionID,
		Name:              "Vivienda",
		AllocationPercent: 56,
		Icon:              "home",
		SortOrder:         1,
		CreatedAt:         time.Now(),
	}

	data, err := json.Marshal(scv)
	if err != nil {
		t.Fatalf("failed to marshal SummaryCategoryView: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	// Should have "section_id", not "category_id"
	if _, ok := m["section_id"]; !ok {
		t.Error("SummaryCategoryView should have 'section_id' key in JSON")
	}
	if _, ok := m["category_id"]; ok {
		t.Error("SummaryCategoryView should NOT have 'category_id' key in JSON")
	}
}

// ==================== ErrorResponse JSON ====================

func TestErrorResponse_MarshalJSON(t *testing.T) {
	errResp := ErrorResponse{Error: "something went wrong"}

	data, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("failed to marshal ErrorResponse: %v", err)
	}

	expected := `{"error":"something went wrong"}`
	if string(data) != expected {
		t.Errorf("ErrorResponse JSON = %s, want %s", string(data), expected)
	}
}

func TestErrorResponse_UnmarshalJSON(t *testing.T) {
	raw := `{"error":"not found"}`

	var errResp ErrorResponse
	if err := json.Unmarshal([]byte(raw), &errResp); err != nil {
		t.Fatalf("failed to unmarshal ErrorResponse: %v", err)
	}

	if errResp.Error != "not found" {
		t.Errorf("Error = %q, want %q", errResp.Error, "not found")
	}
}

// ==================== Profile JSON ====================

func TestProfile_PasswordHashOmitted(t *testing.T) {
	p := Profile{
		ID:           uuid.New(),
		Email:        "test@example.com",
		FullName:     "Test User",
		PasswordHash: "secret_hash_value",
		AuthProvider: "email",
		CreatedAt:    "2026-01-01T00:00:00Z",
		UpdatedAt:    "2026-01-01T00:00:00Z",
	}

	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("failed to marshal Profile: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	// PasswordHash has json:"-" tag, so it should never appear in JSON.
	if _, ok := m["password_hash"]; ok {
		t.Error("password_hash should not appear in JSON output")
	}
	if _, ok := m["-"]; ok {
		t.Error("'-' key should not appear in JSON output")
	}
}

// ==================== SectionTrend JSON ====================

func TestSectionTrend_JSONKeys(t *testing.T) {
	// SectionTrend uses "category_id" and "category_name" JSON keys
	// for frontend compatibility even though the Go fields are SectionID/SectionName.
	st := SectionTrend{
		SectionID:   uuid.New(),
		SectionName: "Necesidades",
		Months:      []MonthlyTrend{{Month: "2026-04-01", TotalSpent: 100}},
	}

	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("failed to marshal SectionTrend: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	if _, ok := m["category_id"]; !ok {
		t.Error("SectionTrend should have 'category_id' key for frontend compatibility")
	}
	if _, ok := m["category_name"]; !ok {
		t.Error("SectionTrend should have 'category_name' key for frontend compatibility")
	}
	// Verify it does NOT use "section_id" / "section_name" in JSON
	if _, ok := m["section_id"]; ok {
		t.Error("SectionTrend should NOT use 'section_id' in JSON")
	}
	if _, ok := m["section_name"]; ok {
		t.Error("SectionTrend should NOT use 'section_name' in JSON")
	}
}

// ==================== TrendsResponse JSON ====================

func TestTrendsResponse_JSONKeys(t *testing.T) {
	// TrendsResponse uses "categories" JSON key for sections data.
	tr := TrendsResponse{
		BudgetID: uuid.New(),
		Sections: []SectionTrend{
			{SectionID: uuid.New(), SectionName: "Test", Months: nil},
		},
	}

	data, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("failed to marshal TrendsResponse: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	if _, ok := m["categories"]; !ok {
		t.Error("TrendsResponse should use 'categories' JSON key for sections")
	}
	if _, ok := m["sections"]; ok {
		t.Error("TrendsResponse should NOT use 'sections' in JSON")
	}
}

// ==================== Collaborator JSON ====================

func TestCollaborator_ProfileOmitempty(t *testing.T) {
	collab := Collaborator{
		ID:       uuid.New(),
		BudgetID: uuid.New(),
		UserID:   uuid.New(),
		Role:     "editor",
		AddedAt:  "2026-01-01T00:00:00Z",
		Profile:  nil,
	}

	data, err := json.Marshal(collab)
	if err != nil {
		t.Fatalf("failed to marshal Collaborator: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	if _, ok := m["profile"]; ok {
		t.Error("profile should be omitted when nil")
	}
}

// ==================== Invite JSON ====================

func TestInvite_OptionalFieldsOmitted(t *testing.T) {
	invite := Invite{
		ID:          uuid.New(),
		BudgetID:    uuid.New(),
		InviteToken: "test-token-123",
		CreatedBy:   uuid.New(),
		UsedBy:      nil,
		UsedAt:      nil,
		ExpiresAt:   "2026-12-31T23:59:59Z",
		CreatedAt:   "2026-01-01T00:00:00Z",
	}

	data, err := json.Marshal(invite)
	if err != nil {
		t.Fatalf("failed to marshal Invite: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal to map: %v", err)
	}

	if _, ok := m["used_by"]; ok {
		t.Error("used_by should be omitted when nil")
	}
	if _, ok := m["used_at"]; ok {
		t.Error("used_at should be omitted when nil")
	}
}

// ==================== BudgetSummary JSON ====================

func TestBudgetSummary_Structure(t *testing.T) {
	summary := BudgetSummary{
		Budget: Budget{
			ID:            uuid.New(),
			Name:          "Test",
			MonthlyIncome: 5000000,
		},
		Sections:    []SectionSummary{},
		TotalBudget: 5000000,
		TotalSpent:  1000000,
	}

	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("failed to marshal BudgetSummary: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	expectedKeys := []string{"budget", "sections", "total_budget", "total_spent"}
	for _, key := range expectedKeys {
		if _, ok := m[key]; !ok {
			t.Errorf("BudgetSummary JSON missing key %q", key)
		}
	}
}

// ==================== InviteInfo JSON ====================

func TestInviteInfo_MarshalJSON(t *testing.T) {
	info := InviteInfo{
		BudgetName:  "Monthly Budget",
		InviterName: "Alice",
		ExpiresAt:   "2026-12-31T23:59:59Z",
		IsExpired:   false,
		IsUsed:      true,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("failed to marshal InviteInfo: %v", err)
	}

	var decoded InviteInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal InviteInfo: %v", err)
	}

	if decoded.BudgetName != info.BudgetName {
		t.Errorf("BudgetName = %q, want %q", decoded.BudgetName, info.BudgetName)
	}
	if decoded.IsExpired != info.IsExpired {
		t.Errorf("IsExpired = %v, want %v", decoded.IsExpired, info.IsExpired)
	}
	if decoded.IsUsed != info.IsUsed {
		t.Errorf("IsUsed = %v, want %v", decoded.IsUsed, info.IsUsed)
	}
}

// ==================== MonthlyTrend JSON ====================

func TestMonthlyTrend_MarshalJSON(t *testing.T) {
	mt := MonthlyTrend{
		Month:      "2026-04",
		TotalSpent: 1234.56,
	}

	data, err := json.Marshal(mt)
	if err != nil {
		t.Fatalf("failed to marshal MonthlyTrend: %v", err)
	}

	var decoded MonthlyTrend
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal MonthlyTrend: %v", err)
	}

	if decoded.Month != mt.Month {
		t.Errorf("Month = %q, want %q", decoded.Month, mt.Month)
	}
	if decoded.TotalSpent != mt.TotalSpent {
		t.Errorf("TotalSpent = %v, want %v", decoded.TotalSpent, mt.TotalSpent)
	}
}
