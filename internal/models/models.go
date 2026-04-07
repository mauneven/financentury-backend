package models

import (
	"time"

	"github.com/google/uuid"
)

// Profile represents a user profile from the Supabase profiles table.
type Profile struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	FullName  string    `json:"full_name"`
	AvatarURL string    `json:"avatar_url"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
}

// Budget represents a user's budget.
type Budget struct {
	ID                  uuid.UUID `json:"id"`
	UserID              uuid.UUID `json:"user_id"`
	Name                string    `json:"name"`
	MonthlyIncome       float64   `json:"monthly_income"`
	Currency            string    `json:"currency"`
	BillingPeriodMonths int       `json:"billing_period_months"`
	Mode                string    `json:"mode"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Category represents a budget category.
type Category struct {
	ID                uuid.UUID `json:"id"`
	BudgetID          uuid.UUID `json:"budget_id"`
	Name              string    `json:"name"`
	AllocationPercent float64   `json:"allocation_percent"`
	Icon              string    `json:"icon"`
	SortOrder         int       `json:"sort_order"`
	CreatedAt         time.Time `json:"created_at"`
}

// Subcategory represents a budget subcategory.
type Subcategory struct {
	ID                uuid.UUID `json:"id"`
	CategoryID        uuid.UUID `json:"category_id"`
	Name              string    `json:"name"`
	AllocationPercent float64   `json:"allocation_percent"`
	Icon              string    `json:"icon"`
	SortOrder         int       `json:"sort_order"`
	CreatedAt         time.Time `json:"created_at"`
}

// Expense represents a budget expense.
type Expense struct {
	ID            uuid.UUID `json:"id"`
	BudgetID      uuid.UUID `json:"budget_id"`
	SubcategoryID uuid.UUID `json:"subcategory_id"`
	Amount        float64   `json:"amount"`
	Description   string    `json:"description"`
	ExpenseDate   string    `json:"expense_date"`
	CreatedAt     time.Time `json:"created_at"`
}

// --- Request/Response DTOs ---

// CreateBudgetRequest is the payload for creating a budget.
type CreateBudgetRequest struct {
	Name                string  `json:"name"`
	MonthlyIncome       float64 `json:"monthly_income"`
	Currency            string  `json:"currency"`
	BillingPeriodMonths int     `json:"billing_period_months"`
	Mode                string  `json:"mode"`
}

// UpdateBudgetRequest is the payload for updating a budget.
type UpdateBudgetRequest struct {
	Name                *string  `json:"name,omitempty"`
	MonthlyIncome       *float64 `json:"monthly_income,omitempty"`
	Currency            *string  `json:"currency,omitempty"`
	BillingPeriodMonths *int     `json:"billing_period_months,omitempty"`
	Mode                *string  `json:"mode,omitempty"`
}

// CreateCategoryRequest is the payload for creating a category.
type CreateCategoryRequest struct {
	Name              string  `json:"name"`
	AllocationPercent float64 `json:"allocation_percent"`
	Icon              string  `json:"icon"`
	SortOrder         int     `json:"sort_order"`
}

// UpdateCategoryRequest is the payload for updating a category.
type UpdateCategoryRequest struct {
	Name              *string  `json:"name,omitempty"`
	AllocationPercent *float64 `json:"allocation_percent,omitempty"`
	Icon              *string  `json:"icon,omitempty"`
	SortOrder         *int     `json:"sort_order,omitempty"`
}

// CreateSubcategoryRequest is the payload for creating a subcategory.
type CreateSubcategoryRequest struct {
	Name              string  `json:"name"`
	AllocationPercent float64 `json:"allocation_percent"`
	Icon              string  `json:"icon"`
	SortOrder         int     `json:"sort_order"`
}

// UpdateSubcategoryRequest is the payload for updating a subcategory.
type UpdateSubcategoryRequest struct {
	Name              *string  `json:"name,omitempty"`
	AllocationPercent *float64 `json:"allocation_percent,omitempty"`
	Icon              *string  `json:"icon,omitempty"`
	SortOrder         *int     `json:"sort_order,omitempty"`
}

// CreateExpenseRequest is the payload for creating an expense.
type CreateExpenseRequest struct {
	SubcategoryID uuid.UUID `json:"subcategory_id"`
	Amount        float64   `json:"amount"`
	Description   string    `json:"description"`
	ExpenseDate   string    `json:"expense_date"`
}

// UpdateExpenseRequest is the payload for updating an expense.
type UpdateExpenseRequest struct {
	SubcategoryID *uuid.UUID `json:"subcategory_id,omitempty"`
	Amount        *float64   `json:"amount,omitempty"`
	Description   *string    `json:"description,omitempty"`
	ExpenseDate   *string    `json:"expense_date,omitempty"`
}

// --- Summary Response Types ---

// SubcategorySummary contains a subcategory with its spending totals.
type SubcategorySummary struct {
	Subcategory     Subcategory `json:"subcategory"`
	AllocatedAmount float64     `json:"allocated_amount"`
	TotalSpent      float64     `json:"total_spent"`
	ExpenseCount    int         `json:"expense_count"`
}

// CategorySummary contains a category with subcategory summaries.
type CategorySummary struct {
	Category        Category             `json:"category"`
	Subcategories   []SubcategorySummary `json:"subcategories"`
	AllocatedAmount float64              `json:"allocated_amount"`
	TotalSpent      float64              `json:"total_spent"`
}

// BudgetSummary is the full budget summary response.
type BudgetSummary struct {
	Budget      Budget            `json:"budget"`
	Categories  []CategorySummary `json:"categories"`
	TotalBudget float64           `json:"total_budget"`
	TotalSpent  float64           `json:"total_spent"`
}

// MonthlyTrend represents spending for a single month.
type MonthlyTrend struct {
	Month      string  `json:"month"`
	TotalSpent float64 `json:"total_spent"`
}

// CategoryTrend contains trends for a single category.
type CategoryTrend struct {
	CategoryID   uuid.UUID      `json:"category_id"`
	CategoryName string         `json:"category_name"`
	Months       []MonthlyTrend `json:"months"`
}

// TrendsResponse is the trends endpoint response.
type TrendsResponse struct {
	BudgetID   uuid.UUID       `json:"budget_id"`
	Categories []CategoryTrend `json:"categories"`
}

// ErrorResponse is a standard error response.
type ErrorResponse struct {
	Error string `json:"error"`
}
