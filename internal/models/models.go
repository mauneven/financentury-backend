package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Profile represents a user profile from the profiles table.
type Profile struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	FullName     string    `json:"full_name"`
	AvatarURL    string    `json:"avatar_url"`
	PasswordHash string    `json:"-"`                        // never sent to client
	AuthProvider string    `json:"auth_provider,omitempty"`
	CreatedAt    string    `json:"created_at"`
	UpdatedAt    string    `json:"updated_at"`
}

// Budget represents a user's budget.
type Budget struct {
	ID                  uuid.UUID `json:"id"`
	UserID              uuid.UUID `json:"user_id"`
	Name                string    `json:"name"`
	Icon                string    `json:"icon"`
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

// Category represents a budget category (child of a Section, stored in the categories table).
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
	CategoryID  uuid.UUID  `json:"category_id"`
	Amount      float64    `json:"amount"`
	Description string     `json:"description"`
	ExpenseDate string     `json:"expense_date"`
	CreatedBy   *uuid.UUID `json:"created_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// UnmarshalJSON handles both the DB column name (subcategory_id) and the
// canonical API field name (category_id) so that expenses read from the
// database deserialize correctly even though the DB column has not been
// renamed yet.
func (e *Expense) UnmarshalJSON(data []byte) error {
	type Alias Expense
	aux := &struct {
		*Alias
		SubcategoryID *uuid.UUID `json:"subcategory_id,omitempty"`
	}{Alias: (*Alias)(e)}

	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	// If category_id was not set but subcategory_id was, use subcategory_id.
	if e.CategoryID == uuid.Nil && aux.SubcategoryID != nil {
		e.CategoryID = *aux.SubcategoryID
	}
	return nil
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
	Icon                string  `json:"icon"`
	MonthlyIncome       float64 `json:"monthly_income"`
	Currency            string  `json:"currency"`
	BillingPeriodMonths int     `json:"billing_period_months"`
	BillingCutoffDay    int     `json:"billing_cutoff_day"`
	Mode                string  `json:"mode"`
}

// UpdateBudgetRequest is the payload for updating a budget.
type UpdateBudgetRequest struct {
	Name                *string  `json:"name,omitempty"`
	Icon                *string  `json:"icon,omitempty"`
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
	CategoryID  uuid.UUID `json:"category_id"`
	Amount      float64   `json:"amount"`
	Description string    `json:"description"`
	ExpenseDate string    `json:"expense_date"`
}

// UpdateExpenseRequest is the payload for updating an expense.
type UpdateExpenseRequest struct {
	CategoryID  *uuid.UUID `json:"category_id,omitempty"`
	Amount      *float64   `json:"amount,omitempty"`
	Description *string    `json:"description,omitempty"`
	ExpenseDate *string    `json:"expense_date,omitempty"`
}

// --- Summary Response Types ---

// SummaryCategoryView is the category representation used in summary responses.
// It maps the DB column "category_id" to the JSON key "section_id" because the
// frontend Category type uses "section_id" to refer to the parent section.
type SummaryCategoryView struct {
	ID                uuid.UUID `json:"id"`
	SectionID         uuid.UUID `json:"section_id"`
	Name              string    `json:"name"`
	AllocationPercent float64   `json:"allocation_percent"`
	Icon              string    `json:"icon"`
	SortOrder         int       `json:"sort_order"`
	CreatedAt         time.Time `json:"created_at"`
}

// UserSpending represents one user's spending within a budget, section, or category.
type UserSpending struct {
	UserID  uuid.UUID `json:"user_id"`
	Profile *Profile  `json:"profile,omitempty"`
	Amount  float64   `json:"amount"`
}

// CategorySummary contains a category with its spending totals.
type CategorySummary struct {
	Category        SummaryCategoryView `json:"category"`
	AllocatedAmount float64             `json:"allocated_amount"`
	TotalSpent      float64             `json:"total_spent"`
	ExpenseCount    int                 `json:"expense_count"`
	SpendingByUser  []UserSpending      `json:"spending_by_user,omitempty"`
}

// SectionSummary contains a section with category summaries.
type SectionSummary struct {
	Section         Section           `json:"section"`
	Categories      []CategorySummary `json:"categories"`
	AllocatedAmount float64           `json:"allocated_amount"`
	TotalSpent      float64           `json:"total_spent"`
	SpendingByUser  []UserSpending    `json:"spending_by_user,omitempty"`
}

// BudgetSummary is the full budget summary response.
type BudgetSummary struct {
	Budget         Budget           `json:"budget"`
	Sections       []SectionSummary `json:"sections"`
	TotalBudget    float64          `json:"total_budget"`
	TotalSpent     float64          `json:"total_spent"`
	SpendingByUser []UserSpending   `json:"spending_by_user,omitempty"`
}

// MonthlyTrend represents spending for a single month.
type MonthlyTrend struct {
	Month      string  `json:"month"`
	TotalSpent float64 `json:"total_spent"`
}

// SectionTrend contains trends for a single section. JSON keys use
// "category_id" / "category_name" because the frontend type calls sections
// "categories" in the trends context.
type SectionTrend struct {
	SectionID   uuid.UUID      `json:"category_id"`
	SectionName string         `json:"category_name"`
	Months      []MonthlyTrend `json:"months"`
}

// TrendsResponse is the trends endpoint response. The JSON key "categories"
// actually contains section-level trend data (matching the frontend type).
type TrendsResponse struct {
	BudgetID uuid.UUID      `json:"budget_id"`
	Sections []SectionTrend `json:"categories"`
}

// BudgetResumePeriod represents the resume for a single billing period.
type BudgetResumePeriod struct {
	PeriodStart string  `json:"period_start"`
	PeriodEnd   string  `json:"period_end"`
	Income      float64 `json:"income"`
	TotalSpent  float64 `json:"total_spent"`
	Balance     float64 `json:"balance"`
}

// BudgetResumeResponse is the budget resume endpoint response.
// For recurring budgets: completed periods with expense data.
// For one-time budgets: a single period from creation to now.
type BudgetResumeResponse struct {
	BudgetID uuid.UUID            `json:"budget_id"`
	OneTime  bool                 `json:"one_time"`
	Periods  []BudgetResumePeriod `json:"periods"`
}

// Session represents an active user session.
type Session struct {
	ID           uuid.UUID  `json:"id"`
	UserID       uuid.UUID  `json:"-"`
	TokenHash    string     `json:"-"`
	IPAddress    string     `json:"ip_address"`
	DeviceType   string     `json:"device_type"`
	Browser      string     `json:"browser"`
	OS           string     `json:"os"`
	IsCurrent    bool       `json:"is_current"`
	CreatedAt    time.Time  `json:"created_at"`
	LastActiveAt time.Time  `json:"last_active_at"`
	ExpiresAt    time.Time  `json:"-"`
	RevokedAt    *time.Time `json:"-"`
}

// ErrorResponse is a standard error response.
type ErrorResponse struct {
	Error string `json:"error"`
}
