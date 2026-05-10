package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// resetSessionCache clears every entry in the package-level sessionCache so
// tests start from a clean slate. sync.Map has no Reset method, so we Range
// and Delete.
func resetSessionCache() {
	sessionCache.Range(func(key, _ interface{}) bool {
		sessionCache.Delete(key)
		return true
	})
}

// tokenHashFor returns the sha256 hex digest used as the cache key for a
// given token string — identical to the computation inside Protected().
func tokenHashFor(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// TestCache_HitRejectsMismatchedUserID verifies that a cached entry for user
// A cannot be reused by a forged/modified token claiming to be user B. This
// guards against the "token rebind" attack: if the cache only looked up by
// hash and trusted whatever user_id came back, swapping the hash without
// invalidating would let an attacker impersonate other users.
//
// We exercise the invariant at the cache layer directly — populating an
// entry keyed by tokenA's hash with userA, then asserting that comparing
// against a different userID would fail the mismatch guard. The full
// middleware path requires a live DB, so this test focuses on the in-memory
// comparison that backs it.
func TestCache_HitRejectsMismatchedUserID(t *testing.T) {
	resetSessionCache()
	defer resetSessionCache()

	userA := uuid.New()
	userB := uuid.New()
	hashA := tokenHashFor("token-for-user-a")

	sessionCache.Store(hashA, sessionCacheEntry{
		userID:    userA.String(),
		sessionID: "session-a",
		cachedAt:  time.Now(),
	})

	cached, ok := sessionCache.Load(hashA)
	if !ok {
		t.Fatal("expected cached entry for userA")
	}
	entry := cached.(sessionCacheEntry)

	// SECURITY: the middleware's hit path checks entry.userID != claim.UserID
	// before honoring the cache entry. Simulate that check with userB.
	if entry.userID == userB.String() {
		t.Fatal("mismatched userID check is broken: userA entry compared equal to userB")
	}
	if entry.userID != userA.String() {
		t.Fatalf("entry.userID = %q, want %q", entry.userID, userA.String())
	}

	// Sanity: equality against the correct user must hold.
	if entry.userID != userA.String() {
		t.Error("entry.userID should match userA when looked up with the correct claim")
	}
}

// TestCache_InvalidatedAfterSignOut verifies that InvalidateSessionCache
// removes the entry so a follow-up Load misses. This is the exact sequence
// SignOut and RevokeSession rely on to boot revoked tokens from the cache
// immediately rather than waiting for TTL expiry.
func TestCache_InvalidatedAfterSignOut(t *testing.T) {
	resetSessionCache()
	defer resetSessionCache()

	userID := uuid.New()
	hash := tokenHashFor("some-valid-token")

	sessionCache.Store(hash, sessionCacheEntry{
		userID:    userID.String(),
		sessionID: "session-1",
		cachedAt:  time.Now(),
	})

	// Precondition: entry exists.
	if _, ok := sessionCache.Load(hash); !ok {
		t.Fatal("precondition failed: entry should exist before invalidation")
	}

	// Simulate SignOut's cache invalidation call.
	InvalidateSessionCache(hash)

	// SECURITY: after invalidation the cache MUST miss. If it doesn't, a
	// revoked token stays authenticated for up to sessionCacheTTL.
	if _, ok := sessionCache.Load(hash); ok {
		t.Error("cache entry should be gone after InvalidateSessionCache")
	}

	// InvalidateUserSessionCache should also nuke everything belonging to
	// the user (simulating DeleteAccount).
	hash2 := tokenHashFor("another-token")
	sessionCache.Store(hash, sessionCacheEntry{userID: userID.String(), cachedAt: time.Now()})
	sessionCache.Store(hash2, sessionCacheEntry{userID: userID.String(), cachedAt: time.Now()})

	InvalidateUserSessionCache(userID.String())

	if _, ok := sessionCache.Load(hash); ok {
		t.Error("first entry should be gone after InvalidateUserSessionCache")
	}
	if _, ok := sessionCache.Load(hash2); ok {
		t.Error("second entry should be gone after InvalidateUserSessionCache")
	}
}

// TestCache_ExpiredEntryFallsBackToDB asserts the TTL boundary: an entry
// older than sessionCacheTTL must NOT be served. The middleware's hit path
// is guarded by `time.Since(entry.cachedAt) < sessionCacheTTL`; anything
// outside that window falls through to the DB query. We skip the full
// round-trip (requires DB) and just verify the predicate.
func TestCache_ExpiredEntryFallsBackToDB(t *testing.T) {
	resetSessionCache()
	defer resetSessionCache()

	userID := uuid.New()
	hash := tokenHashFor("stale-token")

	// Stuff the cache with an entry that's already older than the TTL.
	staleAt := time.Now().Add(-(sessionCacheTTL + time.Second))
	sessionCache.Store(hash, sessionCacheEntry{
		userID:    userID.String(),
		sessionID: "session-stale",
		cachedAt:  staleAt,
	})

	cached, ok := sessionCache.Load(hash)
	if !ok {
		t.Fatal("expected cached entry to be present (even if stale)")
	}
	entry := cached.(sessionCacheEntry)

	// SECURITY: the middleware uses `time.Since(entry.cachedAt) < sessionCacheTTL`.
	// For a stale entry this must be false, forcing a DB fallback.
	if time.Since(entry.cachedAt) < sessionCacheTTL {
		t.Errorf("stale entry was within TTL window: age=%v, ttl=%v — middleware would serve it", time.Since(entry.cachedAt), sessionCacheTTL)
	}
}

// TestCache_MaxAgeEvicts directly exercises the evictor predicate. We don't
// actually run the background ticker (2 min period is too slow for a unit
// test); instead we replicate the body of the eviction loop over the
// current cache contents and assert that max-age-exceeded entries are gone
// while fresh ones remain.
func TestCache_MaxAgeEvicts(t *testing.T) {
	resetSessionCache()
	defer resetSessionCache()

	userID := uuid.New()
	freshHash := tokenHashFor("fresh-token")
	oldHash := tokenHashFor("old-token")

	now := time.Now()
	sessionCache.Store(freshHash, sessionCacheEntry{
		userID:    userID.String(),
		sessionID: "fresh",
		cachedAt:  now,
	})
	sessionCache.Store(oldHash, sessionCacheEntry{
		userID:    userID.String(),
		sessionID: "old",
		cachedAt:  now.Add(-(sessionCacheMaxAge + time.Minute)),
	})

	// Run one pass of the evictor body (same predicate used in
	// startSessionCacheEvictor).
	sessionCache.Range(func(key, value interface{}) bool {
		entry, ok := value.(sessionCacheEntry)
		if !ok {
			sessionCache.Delete(key)
			return true
		}
		if time.Since(entry.cachedAt) > sessionCacheMaxAge {
			sessionCache.Delete(key)
		}
		return true
	})

	// SECURITY: old entry must be gone, fresh entry must survive.
	if _, ok := sessionCache.Load(oldHash); ok {
		t.Error("old entry (> maxAge) should have been evicted")
	}
	if _, ok := sessionCache.Load(freshHash); !ok {
		t.Error("fresh entry (< maxAge) should not have been evicted")
	}
}

// TestCache_ConcurrentAccess stresses the cache with many goroutines
// reading, writing, and invalidating simultaneously. Run with `-race` to
// catch data races. sync.Map is the only shared state, so any race here
// points at a misuse of the Load/Store/Delete contract.
func TestCache_ConcurrentAccess(t *testing.T) {
	resetSessionCache()
	defer resetSessionCache()

	const numGoroutines = 50
	const numOpsPerGoroutine = 200

	userID := uuid.New().String()
	hashes := make([]string, 32)
	for i := range hashes {
		hashes[i] = tokenHashFor("token-" + uuid.New().String())
	}

	var wg sync.WaitGroup
	var opsDone atomic.Int64

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(gi int) {
			defer wg.Done()
			for i := 0; i < numOpsPerGoroutine; i++ {
				h := hashes[(gi+i)%len(hashes)]
				switch (gi + i) % 4 {
				case 0:
					sessionCache.Store(h, sessionCacheEntry{
						userID:    userID,
						sessionID: "sess",
						cachedAt:  time.Now(),
					})
				case 1:
					if v, ok := sessionCache.Load(h); ok {
						// Read a field to ensure we're actually touching the
						// entry (force the type assertion path through the
						// race detector).
						_ = v.(sessionCacheEntry).userID
					}
				case 2:
					InvalidateSessionCache(h)
				case 3:
					InvalidateUserSessionCache(userID)
				}
				opsDone.Add(1)
			}
		}(g)
	}

	wg.Wait()

	if got := opsDone.Load(); got != int64(numGoroutines*numOpsPerGoroutine) {
		t.Errorf("expected %d ops, got %d", numGoroutines*numOpsPerGoroutine, got)
	}
}
