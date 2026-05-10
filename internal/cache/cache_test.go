package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ==================== Cache type (legacy string-key API) ====================

func TestCache_SetAndGet(t *testing.T) {
	c := New()
	bid := uuid.New()
	key := KeyFor(bid, "summary", "user123")
	c.Set(key, bid, []byte("hello"), time.Minute)

	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected hit, got miss")
	}
	if string(got) != "hello" {
		t.Errorf("got %q, want hello", string(got))
	}
}

func TestCache_Expiration(t *testing.T) {
	c := New()
	bid := uuid.New()
	key := KeyFor(bid, "summary")
	c.Set(key, bid, []byte("data"), 10*time.Millisecond)

	time.Sleep(20 * time.Millisecond)

	if _, ok := c.Get(key); ok {
		t.Error("expected expired entry to miss")
	}
}

func TestCache_InvalidateBudget(t *testing.T) {
	c := New()
	bid := uuid.New()
	other := uuid.New()

	c.Set(KeyFor(bid, "summary", "u1"), bid, []byte("a"), time.Minute)
	c.Set(KeyFor(bid, "trends", "u1"), bid, []byte("b"), time.Minute)
	c.Set(KeyFor(other, "summary", "u2"), other, []byte("c"), time.Minute)

	c.InvalidateBudget(bid)

	if _, ok := c.Get(KeyFor(bid, "summary", "u1")); ok {
		t.Error("bid summary should have been invalidated")
	}
	if _, ok := c.Get(KeyFor(bid, "trends", "u1")); ok {
		t.Error("bid trends should have been invalidated")
	}
	if _, ok := c.Get(KeyFor(other, "summary", "u2")); !ok {
		t.Error("other budget entries should survive")
	}
}

func TestCache_ZeroTTLIsNoOp(t *testing.T) {
	c := New()
	bid := uuid.New()
	k := KeyFor(bid, "x")
	c.Set(k, bid, []byte("x"), 0)
	if _, ok := c.Get(k); ok {
		t.Error("zero TTL should not store")
	}
}

func TestCache_Concurrent(t *testing.T) {
	c := New()
	bid := uuid.New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := KeyFor(bid, "k", string(rune('a'+i%26)))
			c.Set(k, bid, []byte("v"), time.Minute)
			_, _ = c.Get(k)
		}(i)
	}
	wg.Wait()
}

func TestKeyFor_Deterministic(t *testing.T) {
	bid := uuid.New()
	k1 := KeyFor(bid, "summary", "user-1")
	k2 := KeyFor(bid, "summary", "user-1")
	if k1 != k2 {
		t.Errorf("keys should match: %s vs %s", k1, k2)
	}
}

func TestCache_Delete(t *testing.T) {
	c := New()
	bid := uuid.New()
	k := KeyFor(bid, "y")
	c.Set(k, bid, []byte("y"), time.Minute)
	c.Delete(k)
	if _, ok := c.Get(k); ok {
		t.Error("entry should be gone after Delete")
	}
}

func TestCache_Stats(t *testing.T) {
	c := New()
	bid := uuid.New()
	c.Set(KeyFor(bid, "a"), bid, []byte("x"), time.Minute)
	_, _ = c.Get(KeyFor(bid, "a"))
	_, _ = c.Get(KeyFor(bid, "missing"))

	st := c.Stats()
	if st.Hits == 0 || st.Misses == 0 {
		t.Errorf("expected hits and misses; got %+v", st)
	}
}

func TestCache_Clear(t *testing.T) {
	c := New()
	bid := uuid.New()
	c.Set(KeyFor(bid, "a"), bid, []byte("x"), time.Minute)
	c.Clear()
	if _, ok := c.Get(KeyFor(bid, "a")); ok {
		t.Error("Clear should drop all entries")
	}
}

// ==================== Key-based package-level API ====================
//
// These tests target the PERF API: Get/Set/Delete/InvalidateBudget/Stats at
// the package level with a typed Key.

// withFreshDefault swaps in a clean Default cache for the duration of the
// test, restoring the original afterwards. Several package-level tests share
// the Default cache so we isolate each one.
func withFreshDefault(t *testing.T) func() {
	t.Helper()
	prev := Default
	SetDefault(New())
	return func() { SetDefault(prev) }
}

func TestPackage_SetAndGet(t *testing.T) {
	defer withFreshDefault(t)()

	k := Key{
		BudgetID: uuid.New(),
		UserID:   uuid.New(),
		Tag:      "summary",
	}
	Set(k, []byte("payload"), time.Minute)

	got, ok := Get(k)
	if !ok {
		t.Fatal("expected hit")
	}
	if string(got) != "payload" {
		t.Errorf("got %q, want payload", string(got))
	}
}

func TestPackage_TTLExpiry(t *testing.T) {
	defer withFreshDefault(t)()

	k := Key{BudgetID: uuid.New(), UserID: uuid.New(), Tag: "trends"}
	Set(k, []byte("v"), 10*time.Millisecond)

	// Before expiry: present.
	if _, ok := Get(k); !ok {
		t.Fatal("entry should be present before TTL")
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := Get(k); ok {
		t.Error("entry should be gone after TTL")
	}
}

func TestPackage_HitMissCounters(t *testing.T) {
	defer withFreshDefault(t)()

	k := Key{BudgetID: uuid.New(), UserID: uuid.New(), Tag: "resume"}
	Set(k, []byte("v"), time.Minute)

	// 3 hits, 2 misses.
	for i := 0; i < 3; i++ {
		_, _ = Get(k)
	}
	missingKey := Key{BudgetID: uuid.New(), UserID: uuid.New(), Tag: "missing"}
	for i := 0; i < 2; i++ {
		_, _ = Get(missingKey)
	}

	hits, misses, size := Stats()
	if hits < 3 {
		t.Errorf("hits = %d, want >= 3", hits)
	}
	if misses < 2 {
		t.Errorf("misses = %d, want >= 2", misses)
	}
	if size != 1 {
		t.Errorf("size = %d, want 1", size)
	}
}

func TestPackage_InvalidateBudget_MultiUser(t *testing.T) {
	// Spec: InvalidateBudget must evict all keys for that budget,
	// regardless of user/tag.
	defer withFreshDefault(t)()

	bid := uuid.New()
	otherBid := uuid.New()

	userA := uuid.New()
	userB := uuid.New()
	userC := uuid.New()

	// Three users cache different tags under the same budget.
	Set(Key{BudgetID: bid, UserID: userA, Tag: "summary"}, []byte("A-summary"), time.Minute)
	Set(Key{BudgetID: bid, UserID: userA, Tag: "trends"}, []byte("A-trends"), time.Minute)
	Set(Key{BudgetID: bid, UserID: userB, Tag: "summary"}, []byte("B-summary"), time.Minute)
	Set(Key{BudgetID: bid, UserID: userC, Tag: "resume"}, []byte("C-resume"), time.Minute)

	// Another budget the same users also hit; must not be disturbed.
	Set(Key{BudgetID: otherBid, UserID: userA, Tag: "summary"}, []byte("other-A"), time.Minute)

	InvalidateBudget(bid)

	// All bid entries are gone...
	for _, k := range []Key{
		{BudgetID: bid, UserID: userA, Tag: "summary"},
		{BudgetID: bid, UserID: userA, Tag: "trends"},
		{BudgetID: bid, UserID: userB, Tag: "summary"},
		{BudgetID: bid, UserID: userC, Tag: "resume"},
	} {
		if _, ok := Get(k); ok {
			t.Errorf("entry %+v should have been invalidated", k)
		}
	}
	// ...but the other budget's entry survives.
	if _, ok := Get(Key{BudgetID: otherBid, UserID: userA, Tag: "summary"}); !ok {
		t.Error("unrelated budget entry should not have been touched")
	}
}

func TestPackage_Delete(t *testing.T) {
	defer withFreshDefault(t)()

	k := Key{BudgetID: uuid.New(), UserID: uuid.New(), Tag: "summary"}
	Set(k, []byte("v"), time.Minute)
	Delete(k)
	if _, ok := Get(k); ok {
		t.Error("entry should be gone after Delete(Key)")
	}
}

func TestPackage_ConcurrentAccess(t *testing.T) {
	defer withFreshDefault(t)()

	bid := uuid.New()
	// Run many goroutines hitting Set/Get/Delete/InvalidateBudget in parallel.
	// The -race flag (in the normal CI test run) will flag any data race.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			uid := uuid.New()
			k := Key{BudgetID: bid, UserID: uid, Tag: "summary"}
			Set(k, []byte("v"), time.Minute)
			_, _ = Get(k)
			if i%10 == 0 {
				InvalidateBudget(bid)
			}
			if i%7 == 0 {
				Delete(k)
			}
		}(i)
	}
	wg.Wait()
}

func TestPackage_ZeroTTLIsNoOp(t *testing.T) {
	defer withFreshDefault(t)()

	k := Key{BudgetID: uuid.New(), UserID: uuid.New(), Tag: "x"}
	Set(k, []byte("v"), 0)
	if _, ok := Get(k); ok {
		t.Error("zero TTL should not store at package level")
	}
}

func TestPackage_NegativeTTLIsNoOp(t *testing.T) {
	defer withFreshDefault(t)()

	k := Key{BudgetID: uuid.New(), UserID: uuid.New(), Tag: "x"}
	Set(k, []byte("v"), -5*time.Second)
	if _, ok := Get(k); ok {
		t.Error("negative TTL should not store")
	}
}

func TestKey_String_IncludesAllFields(t *testing.T) {
	bid := uuid.New()
	uid := uuid.New()
	k := Key{BudgetID: bid, UserID: uid, Tag: "trends"}
	s := k.String()

	if len(s) == 0 {
		t.Fatal("Key.String returned empty")
	}
	// Must contain both UUIDs and the tag so two different keys collide
	// only when every field matches.
	if !contains(s, bid.String()) {
		t.Errorf("Key.String should contain budget id")
	}
	if !contains(s, uid.String()) {
		t.Errorf("Key.String should contain user id")
	}
	if !contains(s, "trends") {
		t.Errorf("Key.String should contain tag")
	}

	// Identical keys => identical strings (cache lookup determinism).
	k2 := Key{BudgetID: bid, UserID: uid, Tag: "trends"}
	if k.String() != k2.String() {
		t.Error("identical keys should produce identical strings")
	}

	// Changing any field changes the string.
	k3 := Key{BudgetID: bid, UserID: uid, Tag: "summary"}
	if k.String() == k3.String() {
		t.Error("different tags should produce different strings")
	}
}

// contains is a tiny substring helper used above to avoid pulling in strings.
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ==================== Overflow eviction ====================
//
// When more than maxEntries (10 000) live entries exist we must evict the
// oldest. Running the full 10 000 in a unit test would be wasteful; we
// verify the behavior by temporarily lowering the expected size via the
// public API only (can't change the const), so this test exercises pressure
// rather than the exact bound. It is still valuable as a smoke test for
// the eviction path.

func TestPackage_OverflowDoesNotPanic(t *testing.T) {
	defer withFreshDefault(t)()

	// Insert 11 000 entries. maxEntries is 10 000 so ~1 000 should be
	// evicted. We don't assert the exact count because the implementation
	// may batch evictions; we assert the size never exceeds the cap.
	bid := uuid.New()
	for i := 0; i < maxEntries+1000; i++ {
		k := Key{BudgetID: bid, UserID: uuid.New(), Tag: fmt.Sprintf("t%d", i)}
		Set(k, []byte("v"), time.Hour)
	}
	_, _, size := Stats()
	if size > int64(maxEntries) {
		t.Errorf("size after overflow = %d, want <= %d", size, maxEntries)
	}
}

// ==================== Overwrite ====================

func TestCache_OverwriteSameKey(t *testing.T) {
	defer withFreshDefault(t)()

	k := Key{BudgetID: uuid.New(), UserID: uuid.New(), Tag: "summary"}
	Set(k, []byte("old"), time.Minute)
	Set(k, []byte("new"), time.Minute)

	got, ok := Get(k)
	if !ok || string(got) != "new" {
		t.Errorf("overwrite failed: got %q ok=%v", string(got), ok)
	}

	// Stats should still show a single entry, not two.
	_, _, size := Stats()
	if size != 1 {
		t.Errorf("size after overwrite = %d, want 1", size)
	}
}

// ==================== InvalidateBudget edge cases ====================

// A non-existent budgetID must be a no-op — no panic, no side effects on
// other budgets' entries. Mutation handlers call InvalidateBudget
// opportunistically after writes; a write that couldn't have been cached
// (first-ever request, or entries already swept) must not crash the app.
func TestCache_InvalidateBudget_NonExistent(t *testing.T) {
	c := New()
	bid := uuid.New()
	other := uuid.New()

	c.Set(KeyFor(bid, "summary"), bid, []byte("v"), time.Minute)

	// Invalidate a budget that was never cached.
	c.InvalidateBudget(other)

	// The real budget's entry must still be present.
	if _, ok := c.Get(KeyFor(bid, "summary")); !ok {
		t.Error("unrelated InvalidateBudget disturbed existing entry")
	}
}

// Hitting InvalidateBudget twice in a row must leave the cache in a
// consistent state; the second call finds an empty bucket and must not
// panic or double-free anything.
func TestCache_InvalidateBudget_Idempotent(t *testing.T) {
	c := New()
	bid := uuid.New()
	c.Set(KeyFor(bid, "summary"), bid, []byte("v"), time.Minute)

	c.InvalidateBudget(bid)
	c.InvalidateBudget(bid) // must be a no-op, not a panic

	if _, ok := c.Get(KeyFor(bid, "summary")); ok {
		t.Error("entry should still be gone after double invalidate")
	}
}

// After InvalidateBudget the per-budget index bucket must also be
// released. Keeping an empty bucket around would leak a small map per
// budget that ever cached anything, which would be an unbounded slow
// leak over the lifetime of the process.
func TestCache_InvalidateBudget_ClearsIndex(t *testing.T) {
	c := New()
	bid := uuid.New()
	c.Set(KeyFor(bid, "summary"), bid, []byte("v"), time.Minute)

	c.InvalidateBudget(bid)

	c.mu.Lock()
	_, stillIndexed := c.budgetIndex[bid.String()]
	c.mu.Unlock()
	if stillIndexed {
		t.Error("budgetIndex bucket should be released after invalidation")
	}
}

// ==================== Delete contract ====================

// Delete must drop the entry from all three structures (map, list, and
// per-budget index) so a follow-up InvalidateBudget doesn't trip over a
// stale reference and so the index doesn't grow forever.
func TestCache_Delete_ClearsBudgetIndex(t *testing.T) {
	c := New()
	bid := uuid.New()
	k := KeyFor(bid, "summary")
	c.Set(k, bid, []byte("v"), time.Minute)
	c.Delete(k)

	c.mu.Lock()
	_, stillIndexed := c.budgetIndex[bid.String()]
	_, stillInMap := c.entries[k]
	size := c.order.Len()
	c.mu.Unlock()

	if stillIndexed {
		t.Error("Delete should release empty per-budget index bucket")
	}
	if stillInMap {
		t.Error("Delete should remove from entries map")
	}
	if size != 0 {
		t.Errorf("Delete should remove from order list: size=%d", size)
	}
}

// Delete of a key that was never set is a no-op. Handlers may call this
// after a conditional lookup; it must not crash on a miss.
func TestCache_Delete_NonExistent(t *testing.T) {
	c := New()
	bid := uuid.New()
	c.Delete(KeyFor(bid, "does-not-exist")) // must not panic
}

// ==================== Touch-on-hit (LRU promotion) ====================

// On hit, the entry must move to the front of the order list so overflow
// eviction picks the genuinely least-recently-used value. We can observe
// this by filling the cache to the cap, touching the oldest entry, and
// then inserting one more — the untouched second-oldest should be the
// one evicted, not the just-touched old one.
func TestCache_TouchOnHit_PromotesEntry(t *testing.T) {
	c := New()
	bid := uuid.New()

	// Insert exactly maxEntries; the first one is the tail / oldest.
	firstKey := KeyFor(bid, "first")
	c.Set(firstKey, bid, []byte("first"), time.Hour)
	secondKey := KeyFor(bid, "second")
	c.Set(secondKey, bid, []byte("second"), time.Hour)
	for i := 2; i < maxEntries; i++ {
		c.Set(KeyFor(bid, "k", string(rune(i))), bid, []byte("x"), time.Hour)
	}

	// Touch the oldest. After this hit, "second" is the oldest.
	if _, ok := c.Get(firstKey); !ok {
		t.Fatal("expected hit on first")
	}

	// One more insert pushes us over the cap; the oldest (now "second")
	// should be evicted, and "first" should survive.
	c.Set(KeyFor(bid, "new"), bid, []byte("new"), time.Hour)

	if _, ok := c.Get(firstKey); !ok {
		t.Error("touched entry was evicted despite being most-recent")
	}
	if _, ok := c.Get(secondKey); ok {
		t.Error("second-oldest untouched entry should have been evicted")
	}
}

// ==================== Sweep / memory bounds ====================

// Sweep reclaims entries past their TTL even when no Get or overflow has
// triggered lazy eviction. This is what prevents idle memory from
// growing unbounded under a write-only workload.
func TestCache_Sweep_RemovesExpired(t *testing.T) {
	c := New()
	bid := uuid.New()

	// Short-lived entry + long-lived entry, no gets so nothing is lazily
	// evicted, no overflow because we're well under the cap.
	shortKey := KeyFor(bid, "short")
	longKey := KeyFor(bid, "long")
	c.Set(shortKey, bid, []byte("s"), 5*time.Millisecond)
	c.Set(longKey, bid, []byte("l"), time.Hour)

	time.Sleep(20 * time.Millisecond)

	c.Sweep()

	// Short entry is gone; long entry survives.
	if _, ok := c.Get(shortKey); ok {
		t.Error("Sweep should have removed expired short-ttl entry")
	}
	if _, ok := c.Get(longKey); !ok {
		t.Error("Sweep should not touch still-valid entries")
	}

	// And the per-budget index should only still reference the long
	// entry (or be empty), never a dangling short-entry reference.
	c.mu.Lock()
	idx := c.budgetIndex[bid.String()]
	_, hasShort := idx[shortKey]
	c.mu.Unlock()
	if hasShort {
		t.Error("Sweep should have cleaned the per-budget index")
	}
}

// The amortized sweep inside setLocked runs every sweepInterval inserts.
// After enough writes we expect expired entries to be reclaimed without
// anyone calling Sweep directly.
func TestCache_AmortizedSweep_RunsFromSet(t *testing.T) {
	c := New()
	bid := uuid.New()

	// Write one entry with a tiny TTL.
	doomedKey := KeyFor(bid, "doomed")
	c.Set(doomedKey, bid, []byte("x"), 5*time.Millisecond)
	time.Sleep(20 * time.Millisecond)

	// Do enough fresh writes to trigger the amortized sweep.
	for i := 0; i < sweepInterval+2; i++ {
		c.Set(KeyFor(bid, "filler", string(rune(i))), bid, []byte("f"), time.Hour)
	}

	// The doomed entry should be gone without anyone calling Get on it
	// and without overflow pressure.
	c.mu.Lock()
	_, stillPresent := c.entries[doomedKey]
	c.mu.Unlock()
	if stillPresent {
		t.Error("amortized sweep did not reclaim expired entry")
	}
}

// ==================== Stats monotonicity ====================

// Hit / miss counters are cumulative and must never decrease over the
// life of a cache instance (important for dashboards that render deltas
// from snapshots). Clear explicitly preserves them; we verify that here.
func TestCache_Stats_Monotonic(t *testing.T) {
	c := New()
	bid := uuid.New()
	k := KeyFor(bid, "m")
	c.Set(k, bid, []byte("v"), time.Minute)

	_, _ = c.Get(k)                   // hit
	_, _ = c.Get(KeyFor(bid, "nope")) // miss
	before := c.Stats()
	c.Clear()
	_, _ = c.Get(k) // miss (Clear dropped it)
	after := c.Stats()

	if after.Hits < before.Hits {
		t.Errorf("hits went backwards: before=%d after=%d", before.Hits, after.Hits)
	}
	if after.Misses < before.Misses {
		t.Errorf("misses went backwards: before=%d after=%d", before.Misses, after.Misses)
	}
	if after.Entries != 0 {
		t.Errorf("entries after Clear = %d, want 0", after.Entries)
	}
}

// ==================== 100-goroutine stress ====================

// Heavy concurrent mix of Set / Get / Delete / InvalidateBudget / Stats
// across 100 goroutines with overlapping keys. Under the -race flag any
// missed locking surfaces as a report; we also assert the final state is
// coherent (size never exceeds the cap, counters add up).
func TestCache_Stress_100Goroutines(t *testing.T) {
	defer withFreshDefault(t)()

	const N = 100
	const itersPerGoroutine = 200
	bid := uuid.New()
	var wg sync.WaitGroup
	wg.Add(N)

	for g := 0; g < N; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < itersPerGoroutine; i++ {
				uid := uuid.New()
				k := Key{BudgetID: bid, UserID: uid, Tag: fmt.Sprintf("t%d", i%16)}
				Set(k, []byte("v"), time.Minute)
				_, _ = Get(k)
				if i%13 == 0 {
					Delete(k)
				}
				if i%29 == 0 {
					InvalidateBudget(bid)
				}
				if i%31 == 0 {
					_, _, _ = Stats()
				}
			}
		}(g)
	}
	wg.Wait()

	// Final assertion: whatever survived must respect the size cap and
	// the per-budget index must still be internally consistent with
	// entries (every indexed key exists in entries, every entry with a
	// budgetID is indexed).
	_, _, size := Stats()
	if size > int64(maxEntries) {
		t.Errorf("size after stress = %d, want <= %d", size, maxEntries)
	}

	Default.mu.Lock()
	for bid, keys := range Default.budgetIndex {
		for k := range keys {
			elem, ok := Default.entries[k]
			if !ok {
				t.Errorf("index references missing key: budget=%s key=%s", bid, k)
				continue
			}
			ev := elem.Value.(*entryValue)
			if ev.budgetID != bid {
				t.Errorf("index mismatch: indexed under %s, entry has budgetID %s", bid, ev.budgetID)
			}
		}
	}
	for _, elem := range Default.entries {
		ev := elem.Value.(*entryValue)
		if ev.budgetID == "" {
			continue
		}
		idx, ok := Default.budgetIndex[ev.budgetID]
		if !ok {
			t.Errorf("entry has budgetID %s but no index bucket", ev.budgetID)
			continue
		}
		if _, ok := idx[ev.key]; !ok {
			t.Errorf("entry %s missing from budgetIndex[%s]", ev.key, ev.budgetID)
		}
	}
	Default.mu.Unlock()
}

// ==================== Key equality / collisions ====================

// Two keys that differ in ANY field must hash to different strings.
// Tag-only collisions would silently let the wrong-shaped payload leak
// into a different endpoint family.
func TestKey_NoCollisionsAcrossFields(t *testing.T) {
	bid1 := uuid.New()
	bid2 := uuid.New()
	uid1 := uuid.New()
	uid2 := uuid.New()

	seen := map[string]Key{}
	cases := []Key{
		{BudgetID: bid1, UserID: uid1, Tag: "summary"},
		{BudgetID: bid1, UserID: uid1, Tag: "trends"},  // tag differs
		{BudgetID: bid1, UserID: uid2, Tag: "summary"}, // user differs
		{BudgetID: bid2, UserID: uid1, Tag: "summary"}, // budget differs
		{BudgetID: bid2, UserID: uid2, Tag: "trends"},  // all differ
	}
	for _, k := range cases {
		s := k.String()
		if dup, ok := seen[s]; ok {
			t.Errorf("collision: %+v and %+v produced same string %q", dup, k, s)
		}
		seen[s] = k
	}
}

// The pre-marshaled byte contract: Get returns the exact slice Set was
// given (no copy). Callers rely on this for the zero-allocation hot
// path; if we ever start defensive-copying, the test catches it so we
// can update the spec deliberately.
func TestCache_PreMarshaledBytes_SameSlice(t *testing.T) {
	c := New()
	bid := uuid.New()
	k := KeyFor(bid, "summary")

	payload := []byte(`{"ok":true}`)
	c.Set(k, bid, payload, time.Minute)

	got, ok := c.Get(k)
	if !ok {
		t.Fatal("expected hit")
	}
	// Same backing array => &got[0] == &payload[0]. This is the
	// zero-copy contract.
	if len(got) == 0 || len(payload) == 0 || &got[0] != &payload[0] {
		t.Error("Get should return the original slice, not a copy")
	}
}
