package handlers

import (
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// ==================== Migration Constants ====================

func TestMigrationConstants(t *testing.T) {
	if maxMigrateBudgets != 20 {
		t.Errorf("maxMigrateBudgets = %d, want 20", maxMigrateBudgets)
	}
	if maxMigrateSectionsPerBudget != 100 {
		t.Errorf("maxMigrateSectionsPerBudget = %d, want 100", maxMigrateSectionsPerBudget)
	}
	if maxMigrateCategoriesPerGroup != 100 {
		t.Errorf("maxMigrateCategoriesPerGroup = %d, want 100", maxMigrateCategoriesPerGroup)
	}
	if maxMigrateExpensesPerBudget != 10000 {
		t.Errorf("maxMigrateExpensesPerBudget = %d, want 10000", maxMigrateExpensesPerBudget)
	}
}

// ==================== validateMigrateSection ====================

func TestValidateMigrateSection_Valid(t *testing.T) {
	ms := MigrateSection{
		Name:              "Necesidades",
		AllocationPercent: 50,
		Icon:              "home",
		SortOrder:         1,
	}
	if err := validateMigrateSection(ms); err != nil {
		t.Errorf("valid section should pass: %v", err)
	}
}

func TestValidateMigrateSection_EmptyName(t *testing.T) {
	ms := MigrateSection{
		Name:              "",
		AllocationPercent: 50,
		Icon:              "home",
	}
	err := validateMigrateSection(ms)
	if err == nil {
		t.Fatal("empty name should fail validation")
	}
	fiberErr, ok := err.(*fiber.Error)
	if !ok {
		t.Fatalf("expected *fiber.Error, got %T", err)
	}
	if fiberErr.Code != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400", fiberErr.Code)
	}
	if !strings.Contains(fiberErr.Message, "section name") {
		t.Errorf("message should mention section name, got: %q", fiberErr.Message)
	}
}

func TestValidateMigrateSection_NameTooLong(t *testing.T) {
	ms := MigrateSection{
		Name:              strings.Repeat("a", maxNameLength+1),
		AllocationPercent: 50,
		Icon:              "home",
	}
	err := validateMigrateSection(ms)
	if err == nil {
		t.Fatal("long name should fail validation")
	}
}

func TestValidateMigrateSection_IconTooLong(t *testing.T) {
	ms := MigrateSection{
		Name:              "Valid",
		AllocationPercent: 50,
		Icon:              strings.Repeat("x", maxIconLength+1),
	}
	err := validateMigrateSection(ms)
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

func TestValidateMigrateSection_AllocationNegative(t *testing.T) {
	ms := MigrateSection{
		Name:              "Valid",
		AllocationPercent: -1,
		Icon:              "home",
	}
	err := validateMigrateSection(ms)
	if err == nil {
		t.Fatal("negative allocation should fail validation")
	}
}

func TestValidateMigrateSection_AllocationOver100(t *testing.T) {
	ms := MigrateSection{
		Name:              "Valid",
		AllocationPercent: 101,
		Icon:              "home",
	}
	err := validateMigrateSection(ms)
	if err == nil {
		t.Fatal("allocation > 100 should fail validation")
	}
}

func TestValidateMigrateSection_AllocationBoundaries(t *testing.T) {
	// 0% should pass
	ms0 := MigrateSection{Name: "Valid", AllocationPercent: 0, Icon: "home"}
	if err := validateMigrateSection(ms0); err != nil {
		t.Errorf("0%% allocation should pass: %v", err)
	}

	// 100% should pass
	ms100 := MigrateSection{Name: "Valid", AllocationPercent: 100, Icon: "home"}
	if err := validateMigrateSection(ms100); err != nil {
		t.Errorf("100%% allocation should pass: %v", err)
	}
}

func TestValidateMigrateSection_TooManyCategories(t *testing.T) {
	cats := make([]MigrateCategory, maxMigrateCategoriesPerGroup+1)
	for i := range cats {
		cats[i] = MigrateCategory{Name: "cat", AllocationPercent: 1, Icon: "a"}
	}
	ms := MigrateSection{
		Name:              "Valid",
		AllocationPercent: 50,
		Icon:              "home",
		Categories:        cats,
	}
	err := validateMigrateSection(ms)
	if err == nil {
		t.Fatal("too many categories should fail validation")
	}
}

func TestValidateMigrateSection_EmptyIcon(t *testing.T) {
	// Empty icon should be valid.
	ms := MigrateSection{
		Name:              "Valid",
		AllocationPercent: 50,
		Icon:              "",
	}
	if err := validateMigrateSection(ms); err != nil {
		t.Errorf("empty icon should pass: %v", err)
	}
}

// ==================== validateMigrateCategory ====================

func TestValidateMigrateCategory_Valid(t *testing.T) {
	mc := MigrateCategory{
		Name:              "Vivienda",
		AllocationPercent: 45,
		Icon:              "home",
		SortOrder:         1,
	}
	if err := validateMigrateCategory(mc); err != nil {
		t.Errorf("valid category should pass: %v", err)
	}
}

func TestValidateMigrateCategory_EmptyName(t *testing.T) {
	mc := MigrateCategory{
		Name:              "",
		AllocationPercent: 45,
		Icon:              "home",
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
		Name:              strings.Repeat("a", maxNameLength+1),
		AllocationPercent: 45,
		Icon:              "home",
	}
	err := validateMigrateCategory(mc)
	if err == nil {
		t.Fatal("long name should fail validation")
	}
}

func TestValidateMigrateCategory_IconTooLong(t *testing.T) {
	mc := MigrateCategory{
		Name:              "Valid",
		AllocationPercent: 45,
		Icon:              strings.Repeat("x", maxIconLength+1),
	}
	err := validateMigrateCategory(mc)
	if err == nil {
		t.Fatal("long icon should fail validation")
	}
}

func TestValidateMigrateCategory_AllocationNegative(t *testing.T) {
	mc := MigrateCategory{
		Name:              "Valid",
		AllocationPercent: -0.1,
		Icon:              "home",
	}
	err := validateMigrateCategory(mc)
	if err == nil {
		t.Fatal("negative allocation should fail validation")
	}
}

func TestValidateMigrateCategory_AllocationOver100(t *testing.T) {
	mc := MigrateCategory{
		Name:              "Valid",
		AllocationPercent: 100.1,
		Icon:              "home",
	}
	err := validateMigrateCategory(mc)
	if err == nil {
		t.Fatal("allocation > 100 should fail validation")
	}
}

func TestValidateMigrateCategory_AllocationBoundaries(t *testing.T) {
	mc0 := MigrateCategory{Name: "Valid", AllocationPercent: 0, Icon: "home"}
	if err := validateMigrateCategory(mc0); err != nil {
		t.Errorf("0%% allocation should pass: %v", err)
	}

	mc100 := MigrateCategory{Name: "Valid", AllocationPercent: 100, Icon: "home"}
	if err := validateMigrateCategory(mc100); err != nil {
		t.Errorf("100%% allocation should pass: %v", err)
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
