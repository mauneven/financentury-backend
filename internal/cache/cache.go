// Package cache provides a minimal in-memory TTL cache with per-entry TTL and
// key-prefix invalidation, used to absorb the hot read load on endpoints like
// /budgets/:id/summary and /budgets/:id/trends that are called on every
// dashboard mount and websocket refresh.
//
// The cache is intentionally simple: entries live for their TTL or until a
// mutation calls InvalidateBudget. Entries are keyed by a composite Key
// (budgetID, userID, tag) so each collaborator of a shared budget gets its
// own cache slot and no cross-user leakage can occur.
//
// Thread-safety: all operations are safe to call from multiple goroutines.
//
// PERF: This package is the centerpiece of the DB-cost-reduction pass:
//   - summary/trends/resume endpoints hit a composite of ~10-20 DB queries
//     each; even a 10-second TTL collapses a burst of dashboard reloads into
//     one DB round-trip instead of N.
//   - entry data is kept as []byte (already-marshaled JSON), so the response
//     path is a zero-allocation fast path on hit: read bytes, write headers,
//     write body. No json.Marshal on hits.
//   - a max-entry cap (10 000) + oldest-first eviction keeps memory bounded
//     under adversarial request patterns. On overflow the oldest entry is
//     removed regardless of remaining TTL.
package cache

import (
	"container/list"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// maxEntries caps the total number of cached entries across all budgets /
// users to keep the memory footprint bounded. With average entry size of a
// few KB, 10 000 entries is well under 100 MB even in the worst case.
const maxEntries = 10_000

// sweepInterval controls how often setLocked opportunistically sweeps
// expired entries. Every N inserts we walk the list tail-first and drop
// anything past its TTL so idle memory doesn't grow unbounded when a
// cohort of entries is written once and then never re-read (purely-stale
// fill). 256 is a sweet spot: rare enough to stay O(1) amortized, frequent
// enough to reclaim well before the 10k overflow cap.
const sweepInterval = 256

// Key is the canonical cache key used by handlers.
//
// BudgetID isolates per-budget caches so InvalidateBudget can drop every
// variant for that budget cheaply. UserID prevents one collaborator's cached
// slice from leaking to another user when access rules differ (currency,
// filtered categories, etc.). Tag distinguishes which endpoint family
// produced the value — summary, trends, resume, me, etc.
//
// A zero-value Key (all-zero UUIDs, empty Tag) is legal; callers should
// still set Tag so different endpoints under the same (budget, user) do not
// overwrite each other.
type Key struct {
	BudgetID uuid.UUID
	UserID   uuid.UUID
	Tag      string
}

// String returns a stable serialized form of the key. It is exported so
// tests can compare keys without depending on the package-internal map
// representation.
func (k Key) String() string {
	// Pre-size exactly: 36 (bid) + 1 (|) + 36 (uid) + 1 (|) + len(tag).
	total := 36 + 1 + 36 + 1 + len(k.Tag)
	b := make([]byte, 0, total)
	b = append(b, k.BudgetID.String()...)
	b = append(b, '|')
	b = append(b, k.UserID.String()...)
	b = append(b, '|')
	b = append(b, k.Tag...)
	return string(b)
}

// Cache is a TTL-based byte-value cache with support for bulk invalidation
// by the budget ID embedded in the key. Zero value is NOT ready — use New.
type Cache struct {
	mu      sync.Mutex // protects all four maps/list below
	entries map[string]*list.Element
	// order keeps entries in insertion order (front = newest, back = oldest)
	// so overflow eviction is O(1) at the tail.
	order *list.List

	// hits/misses are tracked for log-based observability. Atomics so we can
	// read them without taking the main lock.
	hits   atomic.Int64
	misses atomic.Int64

	// budgetIndex maps a budgetID (as string) -> set of keys it participates
	// in. This turns InvalidateBudget from O(n) over all entries to O(k)
	// over just the entries for that budget. Maintained alongside the
	// entries map under the same lock.
	budgetIndex map[string]map[string]struct{}

	// setsSinceSweep counts inserts since the last expired-entry sweep.
	// Guarded by mu.
	setsSinceSweep int
}

// entryValue is what we stash in the list. Keeping the raw bytes plus
// expiry is enough; the list also gives us insertion order for eviction.
type entryValue struct {
	key       string
	data      []byte
	expiresAt time.Time
	budgetID  string
}

// New returns a ready-to-use cache. A single cache instance is typically
// shared across all handlers (see the Default global).
func New() *Cache {
	return &Cache{
		entries:     make(map[string]*list.Element),
		order:       list.New(),
		budgetIndex: make(map[string]map[string]struct{}),
	}
}

// Default is the process-wide cache used by handlers. Tests may replace it
// via SetDefault.
var Default = New()

// SetDefault swaps the global cache. Exposed for tests; production code
// should leave it alone.
func SetDefault(c *Cache) { Default = c }

// KeyFor builds a string cache key from a budget ID and arbitrary parts.
//
// This is the legacy API (string keys); new code should prefer Key + the
// package-level functions. KeyFor is retained because existing tests and a
// small number of callers rely on it and the performance profile is
// identical.
func KeyFor(budgetID uuid.UUID, parts ...string) string {
	total := 36 + 1
	for _, p := range parts {
		total += 1 + len(p)
	}
	b := make([]byte, 0, total)
	b = append(b, budgetID.String()...)
	for _, p := range parts {
		b = append(b, '|')
		b = append(b, p...)
	}
	return string(b)
}

// get fetches an entry by its string key. Shared by the string and Key APIs
// so all hit/miss accounting and eviction logic lives in one place.
func (c *Cache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	elem, ok := c.entries[key]
	if !ok {
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}
	ev := elem.Value.(*entryValue)
	if time.Now().After(ev.expiresAt) {
		// Lazy eviction — drop the expired entry so repeat misses don't
		// keep walking the list.
		c.removeElement(elem)
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}
	// Touch: move to front so recently-accessed entries live longer under
	// overflow pressure. This converts the ordered list from FIFO to a
	// classic LRU-on-access pattern at no extra cost.
	c.order.MoveToFront(elem)
	data := ev.data
	c.mu.Unlock()
	c.hits.Add(1)
	return data, true
}

// set inserts or replaces an entry. Caller must have taken the lock.
func (c *Cache) setLocked(key, budgetID string, data []byte, ttl time.Duration) {
	// Overwrite path: drop the existing list node so insertion order for
	// the new value is "fresh".
	if existing, ok := c.entries[key]; ok {
		c.removeElement(existing)
	}
	ev := &entryValue{
		key:       key,
		data:      data,
		expiresAt: time.Now().Add(ttl),
		budgetID:  budgetID,
	}
	elem := c.order.PushFront(ev)
	c.entries[key] = elem
	if budgetID != "" {
		idx, ok := c.budgetIndex[budgetID]
		if !ok {
			idx = make(map[string]struct{})
			c.budgetIndex[budgetID] = idx
		}
		idx[key] = struct{}{}
	}
	// Amortized background work: every sweepInterval inserts, reclaim any
	// entries that sat idle past their TTL. Without this, a cohort of
	// entries written once and never re-read would linger until the 10k
	// overflow cap kicks in; with it, idle memory stays close to the
	// working set.
	c.setsSinceSweep++
	if c.setsSinceSweep >= sweepInterval {
		c.setsSinceSweep = 0
		c.sweepExpiredLocked(time.Now())
	}
	// Evict the oldest entries until we are back under the cap. Usually
	// this loop runs zero or one times; bulk eviction only happens if a
	// burst of inserts arrives after a long quiet period.
	for c.order.Len() > maxEntries {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.removeElement(back)
	}
}

// sweepExpiredLocked walks the list tail-first removing every entry whose
// TTL has elapsed. Caller must hold c.mu. We stop as soon as we find a
// live entry close to the front because MoveToFront keeps recently-used
// entries near the head; the tail is where the stalest values sit. Note:
// we cannot early-exit purely on "first live entry" because LRU reshuffles
// the list — a recently-touched old entry can land in front of an
// expired, less-recently-touched one. So we walk the whole list.
func (c *Cache) sweepExpiredLocked(now time.Time) {
	for e := c.order.Back(); e != nil; {
		prev := e.Prev()
		ev := e.Value.(*entryValue)
		if now.After(ev.expiresAt) {
			c.removeElement(e)
		}
		e = prev
	}
}

// Sweep proactively removes every entry past its TTL. Safe to call at
// any time; primarily useful to handlers that want to reclaim memory
// after a batch operation, and to tests that want to observe the
// post-sweep state without waiting for the amortized trigger.
func (c *Cache) Sweep() {
	c.mu.Lock()
	c.sweepExpiredLocked(time.Now())
	c.setsSinceSweep = 0
	c.mu.Unlock()
}

// removeElement removes a list element and all index entries for it. Caller
// must hold c.mu.
func (c *Cache) removeElement(elem *list.Element) {
	ev := elem.Value.(*entryValue)
	c.order.Remove(elem)
	delete(c.entries, ev.key)
	if ev.budgetID != "" {
		if idx, ok := c.budgetIndex[ev.budgetID]; ok {
			delete(idx, ev.key)
			if len(idx) == 0 {
				delete(c.budgetIndex, ev.budgetID)
			}
		}
	}
}

// Get returns the cached bytes for a string key if present and not expired.
// Callers must tolerate a miss and compute the value themselves.
func (c *Cache) Get(key string) ([]byte, bool) {
	return c.get(key)
}

// Set stores data under key with the given TTL, tracking it in the per-
// budget index so InvalidateBudget can find it cheaply. A TTL <= 0 is a
// no-op so callers can disable caching for a specific request path without
// branching.
func (c *Cache) Set(key string, budgetID uuid.UUID, data []byte, ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	c.setLocked(key, budgetID.String(), data, ttl)
	c.mu.Unlock()
}

// Delete removes a specific string key. Primarily useful in tests and for
// explicit cache-bust paths.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	if elem, ok := c.entries[key]; ok {
		c.removeElement(elem)
	}
	c.mu.Unlock()
}

// InvalidateBudget drops every entry whose key is associated with the given
// budgetID. Called by mutation handlers on every write to an expense,
// category, or link so the next read rebuilds the summary from fresh DB
// state.
//
// Complexity is O(k) in the number of entries for this budget — the
// per-budget index makes this path cheap enough to call on every write
// without batching. An unknown or never-seen budgetID is a no-op.
func (c *Cache) InvalidateBudget(budgetID uuid.UUID) {
	bid := budgetID.String()
	c.mu.Lock()
	idx, ok := c.budgetIndex[bid]
	if !ok {
		c.mu.Unlock()
		return
	}
	// Walk the per-budget key set, dropping each entry from the main
	// structures. We delete the bucket at the end in one shot; clearing
	// it per-key would churn the map allocator for no gain.
	for key := range idx {
		if elem, ok := c.entries[key]; ok {
			c.order.Remove(elem)
			delete(c.entries, key)
		}
	}
	delete(c.budgetIndex, bid)
	c.mu.Unlock()
}

// Clear drops every entry. Used by tests and by admin-level cache-bust
// endpoints. Hits / misses counters are preserved on purpose: those are
// cumulative observability signals, not state that belongs to the data
// we just dropped.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.entries = make(map[string]*list.Element)
	c.order = list.New()
	c.budgetIndex = make(map[string]map[string]struct{})
	c.setsSinceSweep = 0
	c.mu.Unlock()
}

// StatsSnapshot is the detail struct returned by Cache.Stats on a per-
// instance basis. The package-level Stats() function returns a simpler
// int64 triple for compatibility with the monitoring contract described in
// the PERF doc; keep these two views aligned.
type StatsSnapshot struct {
	Hits    uint64
	Misses  uint64
	Entries int
}

// Stats returns a snapshot of the cache statistics. Primarily used by
// debug/metrics endpoints.
func (c *Cache) Stats() StatsSnapshot {
	c.mu.Lock()
	n := c.order.Len()
	c.mu.Unlock()
	return StatsSnapshot{
		Hits:    uint64(c.hits.Load()),
		Misses:  uint64(c.misses.Load()),
		Entries: n,
	}
}

// --- Package-level API ---------------------------------------------------
//
// These are the functions documented in the PERF design: handler code and
// monitoring glue talk to the Default cache through a Key value instead of
// composing string keys. The call goes straight to Default without extra
// allocations on the hot path (Key.String uses a single make).

// Get returns the cached bytes for k if present and not expired.
// PERF: Hot path for summary / trends / resume / auth-me endpoints.
func Get(k Key) ([]byte, bool) {
	if Default == nil {
		return nil, false
	}
	return Default.get(k.String())
}

// Set stores data under k with the given TTL. ttl <= 0 is a no-op.
// PERF: Callers pre-marshal JSON bytes to avoid reserializing on every hit.
func Set(k Key, data []byte, ttl time.Duration) {
	if Default == nil || ttl <= 0 {
		return
	}
	Default.mu.Lock()
	Default.setLocked(k.String(), k.BudgetID.String(), data, ttl)
	Default.mu.Unlock()
}

// Delete removes k from the default cache if present.
func Delete(k Key) {
	if Default == nil {
		return
	}
	Default.Delete(k.String())
}

// InvalidateBudget removes every entry related to budgetID from Default.
// This is the hook every mutation handler calls so a stale summary never
// outlives the write that changed it.
func InvalidateBudget(budgetID uuid.UUID) {
	if Default == nil {
		return
	}
	Default.InvalidateBudget(budgetID)
}

// Stats returns hit/miss/size as int64 so the monitoring contract matches
// what the dashboards expect. Safe to call without the main lock thanks to
// the atomic counters for hits/misses; size requires a brief Lock.
//
// Return order: hits, misses, size (in that order).
func Stats() (hits, misses, size int64) {
	if Default == nil {
		return 0, 0, 0
	}
	hits = Default.hits.Load()
	misses = Default.misses.Load()
	Default.mu.Lock()
	size = int64(Default.order.Len())
	Default.mu.Unlock()
	return hits, misses, size
}
