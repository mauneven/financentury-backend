package models

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// sampleExpenseJSON_SubcategoryID returns a JSON string for an expense using
// the legacy subcategory_id key.
func sampleExpenseJSON_SubcategoryID() []byte {
	return []byte(`{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"subcategory_id": "11111111-1111-1111-1111-111111111111",
		"amount": 150.50,
		"description": "Groceries at the supermarket",
		"expense_date": "2026-04-10",
		"created_at": "2026-04-10T12:00:00Z",
		"updated_at": "2026-04-10T12:00:00Z"
	}`)
}

// sampleExpenseJSON_CatID returns a JSON string for an expense using
// the canonical category_id key.
func sampleExpenseJSON_CatID() []byte {
	return []byte(`{
		"id": "33333333-3333-3333-3333-333333333333",
		"budget_id": "22222222-2222-2222-2222-222222222222",
		"category_id": "11111111-1111-1111-1111-111111111111",
		"amount": 150.50,
		"description": "Groceries at the supermarket",
		"expense_date": "2026-04-10",
		"created_at": "2026-04-10T12:00:00Z",
		"updated_at": "2026-04-10T12:00:00Z"
	}`)
}

// sampleExpense returns a fully populated Expense struct.
func sampleExpense() Expense {
	createdBy := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	return Expense{
		ID:          uuid.MustParse("33333333-3333-3333-3333-333333333333"),
		BudgetID:    uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		CategoryID:  uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		Amount:      150.50,
		Description: "Groceries at the supermarket",
		ExpenseDate: "2026-04-10",
		CreatedBy:   &createdBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// BenchmarkExpenseUnmarshalJSON benchmarks unmarshaling with subcategory_id
// (exercises the custom UnmarshalJSON fallback path).
func BenchmarkExpenseUnmarshalJSON(b *testing.B) {
	data := sampleExpenseJSON_SubcategoryID()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var exp Expense
		if err := json.Unmarshal(data, &exp); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExpenseUnmarshalJSON_CategoryID benchmarks unmarshaling with
// category_id (the canonical path with no fallback needed).
func BenchmarkExpenseUnmarshalJSON_CategoryID(b *testing.B) {
	data := sampleExpenseJSON_CatID()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var exp Expense
		if err := json.Unmarshal(data, &exp); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExpenseMarshalJSON benchmarks marshaling an Expense to JSON.
func BenchmarkExpenseMarshalJSON(b *testing.B) {
	exp := sampleExpense()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(exp); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExpenseUnmarshalLargeArray benchmarks unmarshaling an array of 1000
// expenses to measure throughput for bulk operations.
func BenchmarkExpenseUnmarshalLargeArray(b *testing.B) {
	// Build a JSON array of 1000 expenses.
	single := `{"id":"33333333-3333-3333-3333-333333333333","budget_id":"22222222-2222-2222-2222-222222222222","category_id":"11111111-1111-1111-1111-111111111111","amount":150.50,"description":"Groceries","expense_date":"2026-04-10","created_at":"2026-04-10T12:00:00Z","updated_at":"2026-04-10T12:00:00Z"}`
	arr := []byte("[")
	for i := 0; i < 1000; i++ {
		if i > 0 {
			arr = append(arr, ',')
		}
		arr = append(arr, single...)
	}
	arr = append(arr, ']')

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var expenses []Expense
		if err := json.Unmarshal(arr, &expenses); err != nil {
			b.Fatal(err)
		}
		if len(expenses) != 1000 {
			b.Fatalf("expected 1000 expenses, got %d", len(expenses))
		}
	}
}

// BenchmarkRoundAmount_Local benchmarks an inline rounding helper equivalent
// to the handlers-package roundAmount function so we have a baseline for the
// rounding cost regardless of package.
func BenchmarkRoundAmount_Local(b *testing.B) {
	roundLocal := func(v float64) float64 {
		return float64(int64(v*100+0.5)) / 100
	}
	values := []float64{0, 1234.5678, -99.999, 0.001, 100.0, 3.456, 2.344, 99.995}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, v := range values {
			_ = roundLocal(v)
		}
	}
}

// BenchmarkExpenseUnmarshal_StandardVsCustom compares standard JSON unmarshal
// (using a plain struct without custom UnmarshalJSON) against the custom
// Expense.UnmarshalJSON to measure the overhead of the subcategory_id fallback.
func BenchmarkExpenseUnmarshal_StandardVsCustom(b *testing.B) {
	data := sampleExpenseJSON_CatID()

	// plainExpense is a copy of Expense without custom UnmarshalJSON.
	type plainExpense struct {
		ID          uuid.UUID  `json:"id"`
		BudgetID    uuid.UUID  `json:"budget_id"`
		CategoryID  uuid.UUID  `json:"category_id"`
		Amount      float64    `json:"amount"`
		Description string     `json:"description"`
		ExpenseDate string     `json:"expense_date"`
		CreatedBy   *uuid.UUID `json:"created_by,omitempty"`
		CreatedAt   time.Time  `json:"created_at"`
		UpdatedAt   time.Time  `json:"updated_at"`
	}

	b.Run("StandardUnmarshal", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var exp plainExpense
			if err := json.Unmarshal(data, &exp); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("CustomUnmarshal", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var exp Expense
			if err := json.Unmarshal(data, &exp); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkExpenseMarshalLargeArray benchmarks marshaling 1000 expenses.
func BenchmarkExpenseMarshalLargeArray(b *testing.B) {
	exp := sampleExpense()
	expenses := make([]Expense, 1000)
	for i := range expenses {
		expenses[i] = exp
		expenses[i].ID = uuid.New()
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(expenses); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBudgetMarshalJSON benchmarks marshaling a Budget to measure typical
// response serialization cost.
func BenchmarkBudgetMarshalJSON(b *testing.B) {
	budget := Budget{
		ID:                  uuid.New(),
		UserID:              uuid.New(),
		Name:                "Monthly Budget",
		MonthlyIncome:       5000000,
		Currency:            "COP",
		BillingPeriodMonths: 1,
		BillingCutoffDay:    1,
		Mode:                "balanced",
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(budget); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkBudgetSummaryMarshalJSON benchmarks marshaling a realistic
// BudgetSummary response with multiple flat categories.
func BenchmarkBudgetSummaryMarshalJSON(b *testing.B) {
	now := time.Now()
	budgetID := uuid.New()
	cats := make([]CategorySummary, 16)
	for i := range cats {
		cats[i] = CategorySummary{
			Category: SummaryCategoryView{
				ID:              uuid.New(),
				BudgetID:        budgetID,
				Name:            fmt.Sprintf("Category %d", i),
				AllocationValue: 250000,
				Icon:            "home",
				SortOrder:       i,
				CreatedAt:       now,
			},
			AllocatedAmount: 250000,
			TotalSpent:      100000,
			ExpenseCount:    15,
		}
	}

	summary := BudgetSummary{
		Budget: Budget{
			ID:                  budgetID,
			UserID:              uuid.New(),
			Name:                "Test Budget",
			MonthlyIncome:       5000000,
			Currency:            "COP",
			BillingPeriodMonths: 1,
			BillingCutoffDay:    1,
			Mode:                "balanced",
			CreatedAt:           now,
			UpdatedAt:           now,
		},
		Categories:  cats,
		TotalBudget: 5000000,
		TotalSpent:  1600000,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(summary); err != nil {
			b.Fatal(err)
		}
	}
}
