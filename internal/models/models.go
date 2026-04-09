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
	BillingCutoffDay    int       `json:"billing_cutoff_day"`
	Mode                string    `json:"mode"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Section represents a budget section (top-level grouping, stored in budget_categories table).
type Section struct {
	ID                uuid.UUID `json:"id"`
	BudgetID          uuid.UUID `json:"budget_id"`
	Name              string    `json:"name"`
	AllocationPercent float64   `json:"allocation_percent"`
	Icon              string    `json:"icon"`
	SortOrder         int       `json:"sort_order"`
	CreatedAt         time.Time `json:"created_at"`
}

// Category represents a budget category (child of a Section, stored in budget_subcategories table).
type Category struct {
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
	ID          uuid.UUID  `json:"id"`
	BudgetID    uuid.UUID  `json:"budget_id"`
	CategoryID  uuid.UUID  `json:"subcategory_id"`
	Amount      float64    `json:"amount"`
	Description string     `json:"description"`
	ExpenseDate string     `json:"expense_date"`
	CreatedBy   *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Collaborator represents a budget collaborator.
type Collaborator struct {
	ID       uuid.UUID `json:"id"`
	BudgetID uuid.UUID `json:"budget_id"`
	UserID   uuid.UUID `json:"user_id"`
	Role     string    `json:"role"`
	AddedAt  string    `json:"added_at"`
	Profile  *Profile  `json:"profile,omitempty"`
}

// Invite represents a budget invite.
type Invite struct {
	ID          uuid.UUID  `json:"id"`
	BudgetID    uuid.UUID  `json:"budget_id"`
	InviteToken string     `json:"invite_token"`
	CreatedBy   uuid.UUID  `json:"created_by"`
	UsedBy      *uuid.UUID `json:"used_by,omitempty"`
	UsedAt      *string    `json:"used_at,omitempty"`
	ExpiresAt   string     `json:"expires_at"`
	CreatedAt   string     `json:"created_at"`
}

// InviteInfo is the public invite preview response.
type InviteInfo struct {
	BudgetName  string `json:"budget_name"`
	InviterName string `json:"inviter_name"`
	ExpiresAt   string `json:"expires_at"`
	IsExpired   bool   `json:"is_expired"`
	IsUsed      bool   `json:"is_used"`
}

// --- Request/Response DTOs ---

// CreateBudgetRequest is the payload for creating a budget.
type CreateBudgetRequest struct {
	Name                string  `json:"name"`
	MonthlyIncome       float64 `json:"monthly_income"`
	Currency            string  `json:"currency"`
	BillingPeriodMonths int     `json:"billing_period_months"`
	BillingCutoffDay    int     `json:"billing_cutoff_day"`
	Mode                string  `json:"mode"`
}

// UpdateBudgetRequest is the payload for updating a budget.
type UpdateBudgetRequest struct {
	Name                *string  `json:"name,omitempty"`
	MonthlyIncome       *float64 `json:"monthly_income,omitempty"`
	Currency            *string  `json:"currency,omitempty"`
	BillingPeriodMonths *int     `json:"billing_period_months,omitempty"`
	BillingCutoffDay    *int     `json:"billing_cutoff_day,omitempty"`
	Mode                *string  `json:"mode,omitempty"`
}

// CreateSectionRequest is the payload for creating a section.
type CreateSectionRequest struct {
	Name              string  `json:"name"`
	AllocationPercent float64 `json:"allocation_percent"`
	Icon              string  `json:"icon"`
	SortOrder         int     `json:"sort_order"`
}

// UpdateSectionRequest is the payload for updating a section.
type UpdateSectionRequest struct {
	Name              *string  `json:"name,omitempty"`
	AllocationPercent *float64 `json:"allocation_percent,omitempty"`
	Icon              *string  `json:"icon,omitempty"`
	SortOrder         *int     `json:"sort_order,omitempty"`
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

// CreateExpenseRequest is the payload for creating an expense.
type CreateExpenseRequest struct {
	CategoryID  uuid.UUID `json:"subcategory_id"`
	Amount      float64   `json:"amount"`
	Description string    `json:"description"`
	ExpenseDate string    `json:"expense_date"`
}

// UpdateExpenseRequest is the payload for updating an expense.
type UpdateExpenseRequest struct {
	CategoryID  *uuid.UUID `json:"subcategory_id,omitempty"`
	Amount      *float64   `json:"amount,omitempty"`
	Description *string    `json:"description,omitempty"`
	ExpenseDate *string    `json:"expense_date,omitempty"`
}

// --- Summary Response Types ---

// CategorySummary contains a category with its spending totals.
type CategorySummary struct {
	Category        Category `json:"category"`
	AllocatedAmount float64  `json:"allocated_amount"`
	TotalSpent      float64  `json:"total_spent"`
	ExpenseCount    int      `json:"expense_count"`
}

// SectionSummary contains a section with category summaries.
type SectionSummary struct {
	Section         Section           `json:"section"`
	Categories      []CategorySummary `json:"categories"`
	AllocatedAmount float64           `json:"allocated_amount"`
	TotalSpent      float64           `json:"total_spent"`
}

// BudgetSummary is the full budget summary response.
type BudgetSummary struct {
	Budget      Budget           `json:"budget"`
	Sections    []SectionSummary `json:"sections"`
	TotalBudget float64          `json:"total_budget"`
	TotalSpent  float64          `json:"total_spent"`
}

// MonthlyTrend represents spending for a single month.
type MonthlyTrend struct {
	Month      string  `json:"month"`
	TotalSpent float64 `json:"total_spent"`
}

// SectionTrend contains trends for a single section.
type SectionTrend struct {
	SectionID   uuid.UUID      `json:"section_id"`
	SectionName string         `json:"section_name"`
	Months      []MonthlyTrend `json:"months"`
}

// TrendsResponse is the trends endpoint response.
type TrendsResponse struct {
	BudgetID uuid.UUID      `json:"budget_id"`
	Sections []SectionTrend `json:"sections"`
}

// ErrorResponse is a standard error response.
type ErrorResponse struct {
	Error string `json:"error"`
}
