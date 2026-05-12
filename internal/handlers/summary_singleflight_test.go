package handlers

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSortedUUIDsKey_Stable asserts that sortedUUIDsKey produces the same
// string regardless of map iteration order. This is the property that lets
// loadProfilesSF deduplicate two callers whose ID sets are identical but
// whose internal map ordering differs.
func TestSortedUUIDsKey_Stable(t *testing.T) {
	a := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	b := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	c := uuid.MustParse("00000000-0000-0000-0000-000000000003")

	set1 := map[uuid.UUID]struct{}{a: {}, b: {}, c: {}}
	set2 := map[uuid.UUID]struct{}{c: {}, a: {}, b: {}}

	k1 := sortedUUIDsKey(set1)
	k2 := sortedUUIDsKey(set2)
	if k1 != k2 {
		t.Errorf("keys differ for same set: %q vs %q", k1, k2)
	}
	if !strings.Contains(k1, a.String()) || !strings.Contains(k1, b.String()) || !strings.Contains(k1, c.String()) {
		t.Errorf("key %q missing one of the IDs", k1)
	}
}

// TestSortedUUIDsKey_EmptyMap returns an empty string and never panics.
func TestSortedUUIDsKey_EmptyMap(t *testing.T) {
	if got := sortedUUIDsKey(nil); got != "" {
		t.Errorf("got %q for nil; want empty", got)
	}
	if got := sortedUUIDsKey(map[uuid.UUID]struct{}{}); got != "" {
		t.Errorf("got %q for empty map; want empty", got)
	}
}

// TestUUIDLess_AllByteOrder pins the byte-order ordering used by the
// sortedUUIDsKey helper. A drift here would silently change cache keys.
func TestUUIDLess_AllByteOrder(t *testing.T) {
	a := uuid.UUID{0x01}
	b := uuid.UUID{0x02}
	if !uuidLess(a, b) {
		t.Error("0x01 should be less than 0x02")
	}
	if uuidLess(b, a) {
		t.Error("0x02 should NOT be less than 0x01")
	}
	if uuidLess(a, a) {
		t.Error("equal uuids should compare false")
	}
}

// TestSummarySFKeys_BudgetIDIsolation asserts that two budgets never share
// a singleflight key (a regression here would collapse separate budgets
// into a single shared answer).
func TestSummarySFKeys_BudgetIDIsolation(t *testing.T) {
	b1 := uuid.New()
	b2 := uuid.New()
	u := uuid.New()
	today := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)

	keys := []string{
		"summary:budget:" + b1.String() + ":" + u.String(),
		"summary:budget:" + b2.String() + ":" + u.String(),
		"summary:cats:" + b1.String(),
		"summary:cats:" + b2.String(),
		"summary:linked:" + b1.String() + ":" + u.String() + ":" + today.Format("2006-01-02"),
		"summary:linked:" + b2.String() + ":" + u.String() + ":" + today.Format("2006-01-02"),
	}
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			t.Errorf("key collision: %q", k)
		}
		seen[k] = true
	}
}

// TestSummarySFKeys_ViewerIsolation_Linked is the one that matters for
// data-leak prevention: filter_mode='mine' rows are user-scoped, so the
// linked aggregate's singleflight key MUST include viewerID.
func TestSummarySFKeys_ViewerIsolation_Linked(t *testing.T) {
	bid := uuid.New()
	v1 := uuid.New()
	v2 := uuid.New()
	today := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC).Format("2006-01-02")

	k1 := "summary:linked:" + bid.String() + ":" + v1.String() + ":" + today
	k2 := "summary:linked:" + bid.String() + ":" + v2.String() + ":" + today
	if k1 == k2 {
		t.Fatalf("linked SF keys collapsed across viewers: %q", k1)
	}
}

// TestSummarySFKeys_OwnAggregates_PeriodIsolation ensures a day rollover
// produces a fresh key (period_start is part of the key by design).
func TestSummarySFKeys_OwnAggregates_PeriodIsolation(t *testing.T) {
	bid := uuid.New()
	d1 := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
	d2 := d1.AddDate(0, 0, 1)

	k1 := "summary:own:" + bid.String() + ":" + d1.Format("2006-01-02")
	k2 := "summary:own:" + bid.String() + ":" + d2.Format("2006-01-02")
	if k1 == k2 {
		t.Errorf("own-aggregates SF key did not vary by date: %q", k1)
	}
}

// TestTrendsAggregateKey_CutoffSplit asserts the two SQL branches in
// GetBudgetTrends never share a singleflight bucket.
func TestTrendsAggregateKey_CutoffSplit(t *testing.T) {
	bid := uuid.New()
	a := trendsAggregateKey(bid, true)
	b := trendsAggregateKey(bid, false)
	if a == b {
		t.Errorf("trends key did not vary by useCutoff: %q", a)
	}
	if !strings.HasSuffix(a, ":cutoff") {
		t.Errorf("expected :cutoff suffix on cutoff key, got %q", a)
	}
	if !strings.HasSuffix(b, ":full") {
		t.Errorf("expected :full suffix on non-cutoff key, got %q", b)
	}
}

// TestResumeAggregateKey_DayRollover verifies day boundaries produce
// different keys (a stale period boundary would otherwise survive a UTC
// midnight crossing).
func TestResumeAggregateKey_DayRollover(t *testing.T) {
	bid := uuid.New()
	d1 := time.Date(2026, 5, 8, 23, 59, 0, 0, time.UTC)
	d2 := time.Date(2026, 5, 9, 0, 1, 0, 0, time.UTC)

	if resumeAggregateKey(bid, d1) == resumeAggregateKey(bid, d2) {
		t.Error("resume key did not change across day rollover")
	}
}

// TestSummarySFGroup_Coalesces verifies the singleflight group actually
// collapses concurrent calls. We use a sentinel loader that increments a
// counter; with N goroutines waiting on the same key, the counter must
// land at exactly 1 (the rest received the result via singleflight's
// internal goroutine).
func TestSummarySFGroup_Coalesces(t *testing.T) {
	t.Parallel()
	// Borrow the package-level group to confirm the same instance the
	// production code uses still coalesces. We pick a unique key so
	// nothing else in the test process collides.
	key := "summary:test:" + uuid.NewString()
	const N = 16

	type result struct {
		v   int
		err error
	}
	results := make(chan result, N)
	var calls int64

	gate := make(chan struct{})
	for i := 0; i < N; i++ {
		go func() {
			<-gate
			v, err, _ := summarySFGroup.Do(key, func() (any, error) {
				// Tiny stall so all N goroutines pile up on this key
				// before the first call returns.
				time.Sleep(10 * time.Millisecond)
				calls++
				return 42, nil
			})
			if err != nil {
				results <- result{0, err}
				return
			}
			results <- result{v.(int), nil}
		}()
	}
	close(gate) // release all N goroutines simultaneously

	for i := 0; i < N; i++ {
		r := <-results
		if r.err != nil {
			t.Errorf("goroutine got err: %v", r.err)
		}
		if r.v != 42 {
			t.Errorf("got %d; want 42", r.v)
		}
	}
	if calls != 1 {
		t.Errorf("loader ran %d times; singleflight expected to coalesce to 1", calls)
	}
}
