package handlers

import (
	"testing"

	"github.com/google/uuid"

	"github.com/the-financial-workspace/backend/internal/models"
)

// These tests exercise the pure-logic aggregation that turns the DB-side
// grouped rows (ownAggRow / linkedAggRow) into the BudgetSummary wire
// shape. They are intentionally DB-free so every invariant listed in
// summary.go can be asserted in a deterministic way — no fixture fixture
// races, no fake database, just synthetic rows.

// mustUUID is a short helper for constructing repeatable UUIDs in tests.
func mustUUID(s string) uuid.UUID {
	return uuid.MustParse(s)
}

// ptrUUID returns a pointer-to-uuid for the given UUID.
func ptrUUID(u uuid.UUID) *uuid.UUID {
	return &u
}

// --- aggregateSummarySpending ---------------------------------------------

// INVARIANT (1,2): own-expense totals roll up to totalSpent and are
// reflected per-category.
func TestAggregateSummarySpending_OwnExpensesOnly(t *testing.T) {
	catA := mustUUID("00000000-0000-0000-0000-0000000000aa")
	catB := mustUUID("00000000-0000-0000-0000-0000000000bb")
	userX := mustUUID("00000000-0000-0000-0000-000000000011")
	userY := mustUUID("00000000-0000-0000-0000-000000000021")

	own := []ownAggRow{
		{categoryID: catA, createdBy: ptrUUID(userX), amount: 100, count: 2},
		{categoryID: catA, createdBy: ptrUUID(userY), amount: 50, count: 1},
		{categoryID: catB, createdBy: ptrUUID(userX), amount: 25, count: 1},
	}

	agg := aggregateSummarySpending(own, nil)

	if agg.totalSpent != 175 {
		t.Errorf("totalSpent = %v, want 175", agg.totalSpent)
	}
	if got := agg.categories[catA].totalSpent; got != 150 {
		t.Errorf("catA.totalSpent = %v, want 150", got)
	}
	if got := agg.categories[catA].count; got != 3 {
		t.Errorf("catA.count = %v, want 3", got)
	}
	if got := agg.categories[catB].totalSpent; got != 25 {
		t.Errorf("catB.totalSpent = %v, want 25", got)
	}
	if got := agg.budgetByUser[userX]; got != 125 {
		t.Errorf("budgetByUser[userX] = %v, want 125", got)
	}
	if got := agg.budgetByUser[userY]; got != 50 {
		t.Errorf("budgetByUser[userY] = %v, want 50", got)
	}
	if _, ok := agg.allUserIDs[userX]; !ok {
		t.Error("allUserIDs must contain userX")
	}
	if _, ok := agg.allUserIDs[userY]; !ok {
		t.Error("allUserIDs must contain userY")
	}
}

// INVARIANT (3): rows whose SQL-side created_by is NULL still count
// toward totalSpent and the category totals, but don't produce a
// spending_by_user row.
func TestAggregateSummarySpending_NullCreatedByNoUserRow(t *testing.T) {
	catA := mustUUID("00000000-0000-0000-0000-00000000aa11")

	own := []ownAggRow{
		{categoryID: catA, createdBy: nil, amount: 80, count: 1},
	}

	agg := aggregateSummarySpending(own, nil)

	if agg.totalSpent != 80 {
		t.Errorf("totalSpent = %v, want 80", agg.totalSpent)
	}
	if got := agg.categories[catA].totalSpent; got != 80 {
		t.Errorf("catA.totalSpent = %v, want 80", got)
	}
	if got := agg.categories[catA].count; got != 1 {
		t.Errorf("catA.count = %v, want 1", got)
	}
	if len(agg.budgetByUser) != 0 {
		t.Errorf("budgetByUser should be empty, got %v", agg.budgetByUser)
	}
	if len(agg.allUserIDs) != 0 {
		t.Errorf("allUserIDs should be empty, got %v", agg.allUserIDs)
	}
}

// INVARIANT (2, 3c): linked filter_mode=all spending contributes to
// totalSpent but NOT to budgetByUser. Contributors are still tracked
// in allUserIDs so loadProfiles covers the per-linked-category
// spending_by_user breakdown (invariant 5).
func TestAggregateSummarySpending_LinkedAll_NotInBudgetByUser(t *testing.T) {
	linkID := mustUUID("00000000-0000-0000-0000-0000000000a1")
	spender1 := mustUUID("00000000-0000-0000-0000-0000000000b1")
	spender2 := mustUUID("00000000-0000-0000-0000-0000000000b2")

	linked := []linkedAggRow{
		{LinkID: linkID, FilterMode: "all", CreatedBy: ptrUUID(spender1), Amount: 40, Count: 2},
		{LinkID: linkID, FilterMode: "all", CreatedBy: ptrUUID(spender2), Amount: 60, Count: 1},
	}

	agg := aggregateSummarySpending(nil, linked)

	if agg.totalSpent != 100 {
		t.Errorf("totalSpent = %v, want 100", agg.totalSpent)
	}
	if len(agg.budgetByUser) != 0 {
		t.Errorf("budgetByUser must stay empty for filter_mode=all, got %v", agg.budgetByUser)
	}
	// Critical: both contributors MUST land in allUserIDs so their
	// profiles get fetched for the per-linked-category breakdown.
	if _, ok := agg.allUserIDs[spender1]; !ok {
		t.Error("allUserIDs must contain spender1 (filter_mode=all contributor)")
	}
	if _, ok := agg.allUserIDs[spender2]; !ok {
		t.Error("allUserIDs must contain spender2 (filter_mode=all contributor)")
	}
}

// INVARIANT (2, 3b): linked filter_mode=mine rolls the viewer's share
// into budgetByUser under the viewer's user_id.
func TestAggregateSummarySpending_LinkedMine_RollsIntoViewer(t *testing.T) {
	linkID := mustUUID("00000000-0000-0000-0000-0000000000c1")
	viewer := mustUUID("00000000-0000-0000-0000-0000000000cc")

	linked := []linkedAggRow{
		{LinkID: linkID, FilterMode: "mine", CreatedBy: ptrUUID(viewer), Amount: 75, Count: 3},
	}

	agg := aggregateSummarySpending(nil, linked)

	if agg.totalSpent != 75 {
		t.Errorf("totalSpent = %v, want 75", agg.totalSpent)
	}
	if got := agg.budgetByUser[viewer]; got != 75 {
		t.Errorf("budgetByUser[viewer] = %v, want 75", got)
	}
	if _, ok := agg.allUserIDs[viewer]; !ok {
		t.Error("viewer must be in allUserIDs")
	}
}

// Comprehensive mix: own expenses, filter_mode=all link (2 contributors),
// filter_mode=mine link (viewer only). Asserts every invariant in one
// scenario so a regression in any branch shows up here.
func TestAggregateSummarySpending_MixedOwnAndLinked(t *testing.T) {
	catA := mustUUID("00000000-0000-0000-0000-0000000000aa")
	viewer := mustUUID("00000000-0000-0000-0000-0000000000cc")
	otherMember := mustUUID("00000000-0000-0000-0000-0000000000dd")
	sourceOnlySpender := mustUUID("00000000-0000-0000-0000-0000000000ee")

	own := []ownAggRow{
		{categoryID: catA, createdBy: ptrUUID(viewer), amount: 300, count: 5},
		{categoryID: catA, createdBy: ptrUUID(otherMember), amount: 200, count: 3},
	}

	linkAll := mustUUID("00000000-0000-0000-0000-000000000f01")
	linkMine := mustUUID("00000000-0000-0000-0000-000000000f02")

	linked := []linkedAggRow{
		// filter_mode=all — two contributors, neither is a target-budget
		// member (sourceOnlySpender); one also happens to be a member
		// (otherMember). Neither should touch budgetByUser.
		{LinkID: linkAll, FilterMode: "all", CreatedBy: ptrUUID(sourceOnlySpender), Amount: 40, Count: 1},
		{LinkID: linkAll, FilterMode: "all", CreatedBy: ptrUUID(otherMember), Amount: 10, Count: 1},
		// filter_mode=mine — viewer's own linked spending.
		{LinkID: linkMine, FilterMode: "mine", CreatedBy: ptrUUID(viewer), Amount: 25, Count: 1},
	}

	agg := aggregateSummarySpending(own, linked)

	wantTotal := float64(300 + 200 + 40 + 10 + 25)
	if agg.totalSpent != wantTotal {
		t.Errorf("totalSpent = %v, want %v", agg.totalSpent, wantTotal)
	}

	// Top-level budgetByUser: viewer = own(300) + mine-linked(25) = 325.
	// otherMember = own(200); the filter_mode=all link MUST NOT show
	// up here.
	if got := agg.budgetByUser[viewer]; got != 325 {
		t.Errorf("budgetByUser[viewer] = %v, want 325", got)
	}
	if got := agg.budgetByUser[otherMember]; got != 200 {
		t.Errorf("budgetByUser[otherMember] = %v, want 200 (filter_mode=all must NOT roll in)", got)
	}
	if _, ok := agg.budgetByUser[sourceOnlySpender]; ok {
		t.Error("budgetByUser must not contain sourceOnlySpender (filter_mode=all contributor)")
	}

	// allUserIDs: every user we might need a profile for. That's
	// viewer + otherMember + sourceOnlySpender.
	for _, uid := range []uuid.UUID{viewer, otherMember, sourceOnlySpender} {
		if _, ok := agg.allUserIDs[uid]; !ok {
			t.Errorf("allUserIDs must contain %s", uid)
		}
	}
}

// INVARIANT (5) regression: LEFT-JOIN rows with no matching expenses
// (Amount=0, CreatedBy=nil) must be a no-op for totalSpent math and
// must not produce user-facing artifacts.
func TestAggregateSummarySpending_LinkedZeroMatchRows(t *testing.T) {
	linkID := mustUUID("00000000-0000-0000-0000-0000000000c2")

	linked := []linkedAggRow{
		// Link exists but has no expenses yet — SQL emits one row with
		// Amount=0, Count=0, CreatedBy=nil via LEFT JOIN.
		{LinkID: linkID, FilterMode: "all", CreatedBy: nil, Amount: 0, Count: 0},
	}

	agg := aggregateSummarySpending(nil, linked)

	if agg.totalSpent != 0 {
		t.Errorf("totalSpent = %v, want 0 for zero-match link", agg.totalSpent)
	}
	if len(agg.budgetByUser) != 0 {
		t.Errorf("budgetByUser = %v, want empty", agg.budgetByUser)
	}
	if len(agg.allUserIDs) != 0 {
		t.Errorf("allUserIDs = %v, want empty", agg.allUserIDs)
	}
}

// INVARIANT (3a): every own-expense contributor with > 0 amount must be
// represented. A user who contributes across multiple categories should
// roll up correctly.
func TestAggregateSummarySpending_MultiCategorySameUser(t *testing.T) {
	catA := mustUUID("00000000-0000-0000-0000-0000000000a1")
	catB := mustUUID("00000000-0000-0000-0000-0000000000b1")
	user := mustUUID("00000000-0000-0000-0000-0000000000c1")

	own := []ownAggRow{
		{categoryID: catA, createdBy: ptrUUID(user), amount: 10, count: 1},
		{categoryID: catB, createdBy: ptrUUID(user), amount: 20, count: 1},
	}

	agg := aggregateSummarySpending(own, nil)

	if got := agg.budgetByUser[user]; got != 30 {
		t.Errorf("budgetByUser[user] = %v, want 30 (10 + 20)", got)
	}
	if got := agg.categories[catA].byUser[user]; got != 10 {
		t.Errorf("catA.byUser[user] = %v, want 10", got)
	}
	if got := agg.categories[catB].byUser[user]; got != 20 {
		t.Errorf("catB.byUser[user] = %v, want 20", got)
	}
}

// --- assembleLinkedSummaries ---------------------------------------------

// newTestLinkedRow is a compact constructor for synthetic linkedAggRow
// records used by the assembleLinkedSummaries tests.
func newTestLinkedRow(linkID, sourceBudget, sourceCat uuid.UUID, mode string,
	creator *uuid.UUID, amount float64, count int) linkedAggRow {
	return linkedAggRow{
		LinkID:                linkID,
		SourceBudgetID:        sourceBudget,
		TargetBudgetID:        mustUUID("00000000-0000-0000-0000-0000000000f1"),
		SourceCategoryID:      sourceCat,
		FilterMode:            mode,
		LinkCreatedBy:         mustUUID("00000000-0000-0000-0000-0000000000aa"),
		SBName:                "Source Budget",
		SBIcon:                "icon",
		SBMonthlyIncome:       1000,
		SBCurrency:            "USD",
		SBBillingPeriodMonths: 1,
		SBBillingCutoffDay:    1,
		SBMode:                "shared",
		SBUserID:              mustUUID("00000000-0000-0000-0000-0000000000aa"),
		SCName:                "Source Cat",
		SCAllocationValue:     200,
		SCIcon:                "icon",
		SCSortOrder:           1,
		SCBudgetID:            sourceBudget,
		CreatedBy:             creator,
		Amount:                amount,
		Count:                 count,
	}
}

// INVARIANT (5): linked_categories preserves link order and carries
// source budget + category metadata verbatim. A link with no spending
// is still emitted (TotalSpent=0).
func TestAssembleLinkedSummaries_PreservesLinkOrderAndMetadata(t *testing.T) {
	link1 := mustUUID("00000000-0000-0000-0000-000000000101")
	link2 := mustUUID("00000000-0000-0000-0000-000000000102")
	sb := mustUUID("00000000-0000-0000-0000-000000000201")
	scA := mustUUID("00000000-0000-0000-0000-000000000301")
	scB := mustUUID("00000000-0000-0000-0000-000000000302")

	rows := []linkedAggRow{
		newTestLinkedRow(link1, sb, scA, "all", nil, 0, 0), // zero-match link
		newTestLinkedRow(link2, sb, scB, "all",
			ptrUUID(mustUUID("00000000-0000-0000-0000-000000000401")), 50, 2),
	}

	buildUS := func(byUser map[uuid.UUID]float64) []models.UserSpending {
		if len(byUser) <= 1 {
			return nil
		}
		return []models.UserSpending{}
	}

	out := assembleLinkedSummaries(rows, buildUS)
	if len(out) != 2 {
		t.Fatalf("want 2 linked summaries, got %d", len(out))
	}
	if out[0].Link.ID != link1 || out[1].Link.ID != link2 {
		t.Errorf("link order not preserved")
	}
	if out[0].Category.TotalSpent != 0 || out[0].Category.ExpenseCount != 0 {
		t.Errorf("zero-match link should have 0 total/0 count, got %+v", out[0].Category)
	}
	if out[1].Category.TotalSpent != 50 || out[1].Category.ExpenseCount != 2 {
		t.Errorf("expected 50/2 for link2, got %+v", out[1].Category)
	}
	// Metadata preserved.
	if out[0].SourceBudget.ID != sb || out[0].Category.Category.ID != scA {
		t.Errorf("source budget / category metadata not preserved on out[0]")
	}
}

// Empty rows — no linked categories at all.
func TestAssembleLinkedSummaries_EmptyInput(t *testing.T) {
	buildUS := func(map[uuid.UUID]float64) []models.UserSpending { return nil }
	out := assembleLinkedSummaries(nil, buildUS)
	if out != nil {
		t.Errorf("expected nil for empty input, got %v", out)
	}
}

// INVARIANT (5): a filter_mode=all link with multiple distinct
// contributors produces a per-user breakdown in the linked category's
// spending_by_user. This is the breakdown the profile-fetch fix
// guarantees isn't empty-Profile.
func TestAssembleLinkedSummaries_FilterAllMultiContributorBreakdown(t *testing.T) {
	link := mustUUID("00000000-0000-0000-0000-000000000501")
	sb := mustUUID("00000000-0000-0000-0000-000000000502")
	sc := mustUUID("00000000-0000-0000-0000-000000000503")
	a := mustUUID("00000000-0000-0000-0000-000000000a01")
	b := mustUUID("00000000-0000-0000-0000-000000000b01")

	rows := []linkedAggRow{
		newTestLinkedRow(link, sb, sc, "all", ptrUUID(a), 40, 1),
		newTestLinkedRow(link, sb, sc, "all", ptrUUID(b), 60, 2),
	}

	// buildUS must mimic the real closure: returns nil for <=1 user.
	buildUS := func(byUser map[uuid.UUID]float64) []models.UserSpending {
		if len(byUser) <= 1 {
			return nil
		}
		us := make([]models.UserSpending, 0, len(byUser))
		for uid, amt := range byUser {
			us = append(us, models.UserSpending{UserID: uid, Amount: amt})
		}
		return us
	}

	out := assembleLinkedSummaries(rows, buildUS)
	if len(out) != 1 {
		t.Fatalf("want 1 linked summary, got %d", len(out))
	}
	if got := out[0].Category.TotalSpent; got != 100 {
		t.Errorf("linked.TotalSpent = %v, want 100", got)
	}
	if got := out[0].Category.ExpenseCount; got != 3 {
		t.Errorf("linked.ExpenseCount = %v, want 3", got)
	}
	if len(out[0].Category.SpendingByUser) != 2 {
		t.Errorf("spending_by_user should have 2 entries, got %d", len(out[0].Category.SpendingByUser))
	}
}

// --- sortUserSpendingDesc ------------------------------------------------

func TestSortUserSpendingDesc(t *testing.T) {
	u1 := mustUUID("00000000-0000-0000-0000-000000000001")
	u2 := mustUUID("00000000-0000-0000-0000-000000000002")
	u3 := mustUUID("00000000-0000-0000-0000-000000000003")

	us := []models.UserSpending{
		{UserID: u1, Amount: 10},
		{UserID: u2, Amount: 30},
		{UserID: u3, Amount: 20},
	}
	sortUserSpendingDesc(us)
	if us[0].UserID != u2 || us[1].UserID != u3 || us[2].UserID != u1 {
		t.Errorf("descending sort failed: %v", us)
	}
}

func TestSortUserSpendingDesc_EmptyAndSingle(t *testing.T) {
	var empty []models.UserSpending
	sortUserSpendingDesc(empty) // must not panic

	single := []models.UserSpending{{Amount: 5}}
	sortUserSpendingDesc(single)
	if single[0].Amount != 5 {
		t.Error("single element got clobbered")
	}
}
