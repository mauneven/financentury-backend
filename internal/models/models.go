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

// Category represents a flat budget category belonging directly to a budget.
// The former two-level Section -> Category hierarchy has been collapsed into
// this single table (budget_categories) with a max of 50 rows per budget.
type Category struct {
	ID              uuid.UUID `json:"id"`
	BudgetID        uuid.UUID `json:"budget_id"`
	Name            string    `json:"name"`
	AllocationValue float64   `json:"allocation_value"`
	Icon            string    `json:"icon"`
	SortOrder       int       `json:"sort_order"`
	CreatedAt       time.Time `json:"created_at"`
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

// UnmarshalJSON handles legacy payloads that still carry the old
// "subcategory_id" column name by mapping it to the canonical "category_id"
// field. New payloads use "category_id" directly.
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

// BudgetLink represents a cross-budget link from a source category to a
// target budget. The flat model means every link is now at category
// granularity — section-level links no longer exist.
type BudgetLink struct {
	ID               uuid.UUID `json:"id"`
	SourceBudgetID   uuid.UUID `json:"source_budget_id"`
	TargetBudgetID   uuid.UUID `json:"target_budget_id"`
	SourceCategoryID uuid.UUID `json:"source_category_id"`
	FilterMode       string    `json:"filter_mode"`
	CreatedBy        uuid.UUID `json:"created_by"`
	CreatedAt        time.Time `json:"created_at"`
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

// CreateCategoryRequest is the payload for creating a category.
type CreateCategoryRequest struct {
	Name            string  `json:"name"`
	AllocationValue float64 `json:"allocation_value"`
	Icon            string  `json:"icon"`
	SortOrder       int     `json:"sort_order"`
}

// UpdateCategoryRequest is the payload for updating a category.
type UpdateCategoryRequest struct {
	Name            *string  `json:"name,omitempty"`
	AllocationValue *float64 `json:"allocation_value,omitempty"`
	Icon            *string  `json:"icon,omitempty"`
	SortOrder       *int     `json:"sort_order,omitempty"`
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
// It mirrors Category one-for-one now that the model is flat.
type SummaryCategoryView struct {
	ID              uuid.UUID `json:"id"`
	BudgetID        uuid.UUID `json:"budget_id"`
	Name            string    `json:"name"`
	AllocationValue float64   `json:"allocation_value"`
	Icon            string    `json:"icon"`
	SortOrder       int       `json:"sort_order"`
	CreatedAt       time.Time `json:"created_at"`
}

// UserSpending represents one user's spending within a budget or category.
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

// LinkedCategorySummary is a category summary for linked content in a target
// budget, together with the link metadata and source-budget reference.
type LinkedCategorySummary struct {
	Link         BudgetLink      `json:"link"`
	SourceBudget Budget          `json:"source_budget"`
	Category     CategorySummary `json:"category"`
}

// BudgetSummary is the full budget summary response.
type BudgetSummary struct {
	Budget           Budget                  `json:"budget"`
	Categories       []CategorySummary       `json:"categories"`
	LinkedCategories []LinkedCategorySummary `json:"linked_categories,omitempty"`
	TotalBudget      float64                 `json:"total_budget"`
	TotalSpent       float64                 `json:"total_spent"`
	SpendingByUser   []UserSpending          `json:"spending_by_user,omitempty"`
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

// DisplayOrder stores per-user visual ordering for a given scope.
type DisplayOrder struct {
	ID         uuid.UUID       `json:"id"`
	UserID     uuid.UUID       `json:"user_id"`
	ScopeKey   string          `json:"scope_key"`
	OrderedIDs json.RawMessage `json:"ordered_ids"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// SaveDisplayOrderRequest is the payload for saving a display order.
type SaveDisplayOrderRequest struct {
	ScopeKey   string   `json:"scope_key"`
	OrderedIDs []string `json:"ordered_ids"`
}

// ErrorResponse is a standard error response.
type ErrorResponse struct {
	Error string `json:"error"`
}
