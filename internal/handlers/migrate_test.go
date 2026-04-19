package handlers

import (
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ==================== Migration Constants ====================

func TestMigrationConstants(t *testing.T) {
	if maxMigrateBudgets != 20 {
		t.Errorf("maxMigrateBudgets = %d, want 20", maxMigrateBudgets)
	}
	if maxMigrateCategoriesPerBudget != 50 {
		t.Errorf("maxMigrateCategoriesPerBudget = %d, want 50", maxMigrateCategoriesPerBudget)
	}
	if maxMigrateExpensesPerBudget != 10000 {
		t.Errorf("maxMigrateExpensesPerBudget = %d, want 10000", maxMigrateExpensesPerBudget)
	}
}

// ==================== validateMigrateCategory ====================

func TestValidateMigrateCategory_Valid(t *testing.T) {
	mc := MigrateCategory{
		Name:            "Vivienda",
		AllocationValue: 45,
		Icon:            "home",
		SortOrder:       1,
	}
	if err := validateMigrateCategory(mc); err != nil {
		t.Errorf("valid category should pass: %v", err)
	}
}

func TestValidateMigrateCategory_EmptyName(t *testing.T) {
	mc := MigrateCategory{
		Name:            "",
		AllocationValue: 45,
		Icon:            "home",
	}
	err := validateMigrateCategory(mc)
	if err == nil {
		t.Fatal("empty name should fail validation")
	}
	fiberErr, ok := err.(*fiber.Error)
	if !ok {
		t.Fatalf("expected *fiber.Error, got %T", err)
	}
	if !strings.Contains(fiberErr.Message, "category name") {
		t.Errorf("message should mention category name, got: %q", fiberErr.Message)
	}
}

func TestValidateMigrateCategory_NameTooLong(t *testing.T) {
	mc := MigrateCategory{
		Name:            strings.Repeat("a", maxNameLength+1),
		AllocationValue: 45,
		Icon:            "home",
	}
	err := validateMigrateCategory(mc)
	if err == nil {
		t.Fatal("long name should fail validation")
	}
}

func TestValidateMigrateCategory_IconTooLong(t *testing.T) {
	mc := MigrateCategory{
		Name:            "Valid",
		AllocationValue: 45,
		Icon:            strings.Repeat("x", maxIconLength+1),
	}
	err := validateMigrateCategory(mc)
	if err == nil {
		t.Fatal("long icon should fail validation")
	}
	fiberErr, ok := err.(*fiber.Error)
	if !ok {
		t.Fatalf("expected *fiber.Error, got %T", err)
	}
	if !strings.Contains(fiberErr.Message, "icon") {
		t.Errorf("message should mention icon, got: %q", fiberErr.Message)
	}
}

func TestValidateMigrateCategory_AllocationNegative(t *testing.T) {
	mc := MigrateCategory{
		Name:            "Valid",
		AllocationValue: -0.1,
		Icon:            "home",
	}
	err := validateMigrateCategory(mc)
	if err == nil {
		t.Fatal("negative allocation should fail validation")
	}
}

func TestValidateMigrateCategory_AllocationOver100(t *testing.T) {
	// AllocationValue is a monetary amount (not a percent), so values above
	// 100 are valid.
	mc := MigrateCategory{
		Name:            "Valid",
		AllocationValue: 100.1,
		Icon:            "home",
	}
	if err := validateMigrateCategory(mc); err != nil {
		t.Errorf("value > 100 should pass for monetary allocation: %v", err)
	}
}

func TestValidateMigrateCategory_AllocationBoundaries(t *testing.T) {
	mc0 := MigrateCategory{Name: "Valid", AllocationValue: 0, Icon: "home"}
	if err := validateMigrateCategory(mc0); err != nil {
		t.Errorf("0 allocation should pass: %v", err)
	}

	mc100 := MigrateCategory{Name: "Valid", AllocationValue: 100, Icon: "home"}
	if err := validateMigrateCategory(mc100); err != nil {
		t.Errorf("positive allocation should pass: %v", err)
	}
}

func TestValidateMigrateCategory_EmptyIcon(t *testing.T) {
	// Empty icon should be valid.
	mc := MigrateCategory{
		Name:            "Valid",
		AllocationValue: 50,
		Icon:            "",
	}
	if err := validateMigrateCategory(mc); err != nil {
		t.Errorf("empty icon should pass: %v", err)
	}
}

// ==================== validateMigrateExpense ====================

func TestValidateMigrateExpense_Valid(t *testing.T) {
	me := MigrateExpense{
		Amount:          100.50,
		Description:     "Groceries",
		ExpenseDate:     "2026-04-10",
		LocalCategoryID: "cat-1",
	}
	if err := validateMigrateExpense(me); err != nil {
		t.Errorf("valid expense should pass: %v", err)
	}
}

func TestValidateMigrateExpense_ZeroAmount(t *testing.T) {
	me := MigrateExpense{Amount: 0, Description: "Free"}
	err := validateMigrateExpense(me)
	if err == nil {
		t.Fatal("zero amount should fail validation")
	}
	fiberErr, ok := err.(*fiber.Error)
	if !ok {
		t.Fatalf("expected *fiber.Error, got %T", err)
	}
	if !strings.Contains(fiberErr.Message, "positive") {
		t.Errorf("message should mention positive, got: %q", fiberErr.Message)
	}
}

func TestValidateMigrateExpense_NegativeAmount(t *testing.T) {
	me := MigrateExpense{Amount: -50, Description: "Negative"}
	err := validateMigrateExpense(me)
	if err == nil {
		t.Fatal("negative amount should fail validation")
	}
}

func TestValidateMigrateExpense_AmountExceedsMax(t *testing.T) {
	me := MigrateExpense{Amount: maxAmountValue + 1, Description: "Huge"}
	err := validateMigrateExpense(me)
	if err == nil {
		t.Fatal("amount exceeding max should fail validation")
	}
	fiberErr, ok := err.(*fiber.Error)
	if !ok {
		t.Fatalf("expected *fiber.Error, got %T", err)
	}
	if !strings.Contains(fiberErr.Message, "maximum") {
		t.Errorf("message should mention maximum, got: %q", fiberErr.Message)
	}
}

func TestValidateMigrateExpense_AmountAtMax(t *testing.T) {
	me := MigrateExpense{Amount: maxAmountValue, Description: "Max"}
	if err := validateMigrateExpense(me); err != nil {
		t.Errorf("amount at max should pass: %v", err)
	}
}

func TestValidateMigrateExpense_DescriptionTooLong(t *testing.T) {
	me := MigrateExpense{
		Amount:      100,
		Description: strings.Repeat("a", maxDescriptionLength+1),
	}
	err := validateMigrateExpense(me)
	if err == nil {
		t.Fatal("long description should fail validation")
	}
	fiberErr, ok := err.(*fiber.Error)
	if !ok {
		t.Fatalf("expected *fiber.Error, got %T", err)
	}
	if !strings.Contains(fiberErr.Message, "description") {
		t.Errorf("message should mention description, got: %q", fiberErr.Message)
	}
}

func TestValidateMigrateExpense_EmptyDescription(t *testing.T) {
	me := MigrateExpense{Amount: 100, Description: ""}
	if err := validateMigrateExpense(me); err != nil {
		t.Errorf("empty description should pass: %v", err)
	}
}

func TestValidateMigrateExpense_InvalidDate(t *testing.T) {
	me := MigrateExpense{Amount: 100, ExpenseDate: "not-a-date"}
	err := validateMigrateExpense(me)
	if err == nil {
		t.Fatal("invalid date should fail validation")
	}
	fiberErr, ok := err.(*fiber.Error)
	if !ok {
		t.Fatalf("expected *fiber.Error, got %T", err)
	}
	if !strings.Contains(fiberErr.Message, "date") {
		t.Errorf("message should mention date, got: %q", fiberErr.Message)
	}
}

func TestValidateMigrateExpense_EmptyDate(t *testing.T) {
	// Empty date is allowed (defaults to now in the handler).
	me := MigrateExpense{Amount: 100, ExpenseDate: ""}
	if err := validateMigrateExpense(me); err != nil {
		t.Errorf("empty date should pass: %v", err)
	}
}

func TestValidateMigrateExpense_ValidDate(t *testing.T) {
	me := MigrateExpense{Amount: 100, ExpenseDate: "2026-04-10"}
	if err := validateMigrateExpense(me); err != nil {
		t.Errorf("valid date should pass: %v", err)
	}
}

// ==================== Category Cap (50 per budget) Enforcement ====================

func TestMigrate_CategoryCapConstant(t *testing.T) {
	// The flat model enforces a maximum of 50 categories per migrated budget.
	if maxMigrateCategoriesPerBudget != 50 {
		t.Errorf("maxMigrateCategoriesPerBudget = %d, want 50", maxMigrateCategoriesPerBudget)
	}
}

func TestMigrate_RejectsBudgetWithTooManyCategories(t *testing.T) {
	// migrateSingleBudget must short-circuit a budget carrying more than
	// maxMigrateCategoriesPerBudget categories before any DB calls.
	cats := make([]MigrateCategory, maxMigrateCategoriesPerBudget+1)
	for i := range cats {
		cats[i] = MigrateCategory{
			Name:            "cat",
			AllocationValue: 1,
			Icon:            "",
			SortOrder:       i,
			LocalID:         "local-cat",
		}
	}
	mb := MigrateBudget{
		Name:          "Too Many Categories",
		MonthlyIncome: 5000000,
		Currency:      "COP",
		Mode:          "manual",
		Categories:    cats,
	}
	_, err := migrateSingleBudget(uuid.Nil, mb)
	if err == nil {
		t.Fatal("expected error when exceeding category cap, got nil")
	}
	fiberErr, ok := err.(*fiber.Error)
	if !ok {
		t.Fatalf("expected *fiber.Error, got %T", err)
	}
	if fiberErr.Code != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400", fiberErr.Code)
	}
	if !strings.Contains(fiberErr.Message, "categories") {
		t.Errorf("message should mention categories, got: %q", fiberErr.Message)
	}
}

func TestMigrate_AllowsExactCategoryCap(t *testing.T) {
	// Exactly maxMigrateCategoriesPerBudget categories should pass validation
	// (DB insert may fail because there is no DB connection in unit tests, but
	// we just want to prove the category-count check allows exactly N).
	cats := make([]MigrateCategory, maxMigrateCategoriesPerBudget)
	for i := range cats {
		cats[i] = MigrateCategory{
			Name:            "cat",
			AllocationValue: 1,
			Icon:            "",
			SortOrder:       i,
			LocalID:         "local-cat",
		}
	}
	mb := MigrateBudget{
		Name:          "At Cap",
		MonthlyIncome: 5000000,
		Currency:      "COP",
		Mode:          "manual",
		Categories:    cats,
	}
	_, err := migrateSingleBudget(uuid.Nil, mb)
	// If a DB error surfaces, it should NOT be the "too many categories" one.
	if err != nil {
		if fiberErr, ok := err.(*fiber.Error); ok {
			if strings.Contains(fiberErr.Message, "too many categories") {
				t.Errorf("exactly %d categories should not trip the cap, got: %q",
					maxMigrateCategoriesPerBudget, fiberErr.Message)
			}
		}
	}
}

// ==================== MigrateRequest Types ====================

func TestMigrateRequest_StructFields(t *testing.T) {
	req := MigrateRequest{
		Budgets: []MigrateBudget{
			{
				Name:          "Test Budget",
				MonthlyIncome: 5000000,
				Currency:      "COP",
				Mode:          "manual",
				Categories: []MigrateCategory{
					{Name: "Cat 1", AllocationValue: 100, LocalID: "cat-1"},
				},
				Expenses: []MigrateExpense{
					{LocalCategoryID: "cat-1", Amount: 100, Description: "Test"},
				},
			},
		},
	}

	if len(req.Budgets) != 1 {
		t.Error("should have 1 budget")
	}
	if len(req.Budgets[0].Categories) != 1 {
		t.Error("should have 1 category")
	}
	if len(req.Budgets[0].Expenses) != 1 {
		t.Error("should have 1 expense")
	}
}
