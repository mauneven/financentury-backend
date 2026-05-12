package handlers

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"

	"github.com/the-financial-workspace/backend/internal/models"
	rediscache "github.com/the-financial-workspace/backend/internal/redis"
)

// PERF: WS-driven invalidation produces "thundering herds":
//   - 1 expense write fires invalidateBudget(budgetID) AND a WS broadcast.
//   - Every connected client reacts to the broadcast by refetching summary,
//     trends and resume. With 50 collaborators or 50 open tabs that's 50
//     simultaneous summary builders racing the in-process cache (which was
//     just emptied by the invalidation).
//
// singleflight collapses each (key) into ONE in-flight call; the rest wait
// on its result. The downstream cache.Set then absorbs the next 30s of
// requests via the existing in-process cache. Net effect: a 50-tab burst
// hits the DB exactly once instead of fifty times for the heavy
// aggregation queries.
//
// We attach singleflight at the level that performs the heavy SQL work
// (budget+access, own aggregates, linked aggregates, profile batch) so the
// SQL bodies in summary.go stay untouched and the call sites get a
// drop-in replacement.

// summarySFGroup deduplicates concurrent calls to loadBudgetWithAccess and
// the aggregate loaders. Keys embed the user id where the underlying
// query has user-scoped filtering (linked filter_mode=mine narrows by
// viewer; access check is per-user) so two viewers of the same budget
// don't accidentally share the wrong result.
//
// PERF: a single shared group keeps memory overhead tiny — singleflight
// only allocates while a key is in flight.
var summarySFGroup singleflight.Group

// trendsSFGroup deduplicates the trends aggregate query. Trends are
// view-agnostic (same answer for owner and collaborator), so the key is
// just budget+billing_period_kind.
var trendsSFGroup singleflight.Group

// resumeSFGroup deduplicates the resume aggregation. The full result
// depends on the user's local "today" boundary so we include it in the
// key — otherwise a UTC-only viewer could see an off-by-one period roll
// from another user's locale.
var resumeSFGroup singleflight.Group

// loadBudgetWithAccessSF wraps loadBudgetWithAccess in a singleflight call.
// On a cache miss for the same (budgetID, userID), only the first
// goroutine talks to Postgres; the rest receive the same *models.Budget
// (or error).
func loadBudgetWithAccessSF(ctx context.Context, budgetID, userID uuid.UUID) (*models.Budget, error) {
	key := "summary:budget:" + budgetID.String() + ":" + userID.String()
	v, err, _ := summarySFGroup.Do(key, func() (any, error) {
		return loadBudgetWithAccess(ctx, budgetID, userID)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.(*models.Budget), nil
}

// loadBudgetCategoriesSF wraps loadBudgetCategories. Category lists are
// shared across owner and collaborators so the key omits userID.
func loadBudgetCategoriesSF(ctx context.Context, budgetID uuid.UUID) ([]models.Category, error) {
	key := "summary:cats:" + budgetID.String()
	v, err, _ := summarySFGroup.Do(key, func() (any, error) {
		return loadBudgetCategories(ctx, budgetID)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.([]models.Category), nil
}

// loadOwnExpenseAggregatesSF wraps loadOwnExpenseAggregates. Per-period
// aggregates are shared across viewers, so userID is not part of the key
// — but the period-start (truncated to date) IS, since a day rollover
// changes the answer.
func loadOwnExpenseAggregatesSF(
	ctx context.Context,
	budgetID uuid.UUID,
	periodStart time.Time,
	oneTime bool,
) ([]ownAggRow, error) {
	key := "summary:own:" + budgetID.String() + ":" + periodStart.Format("2006-01-02")
	if oneTime {
		key += ":one"
	}
	v, err, _ := summarySFGroup.Do(key, func() (any, error) {
		return loadOwnExpenseAggregates(ctx, budgetID, periodStart, oneTime)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.([]ownAggRow), nil
}

// loadLinkedAggregatesSF wraps loadLinkedAggregates.
//
// IMPORTANT: filter_mode='mine' rows are gated by viewerID inside the SQL
// WHERE clause, so different viewers see different rows. The
// singleflight key MUST include viewerID — collapsing across viewers
// would leak filter_mode=mine spending across collaborators.
func loadLinkedAggregatesSF(
	ctx context.Context,
	targetBudgetID, viewerID uuid.UUID,
	userToday time.Time,
) ([]linkedAggRow, error) {
	key := "summary:linked:" + targetBudgetID.String() +
		":" + viewerID.String() +
		":" + userToday.Format("2006-01-02")
	v, err, _ := summarySFGroup.Do(key, func() (any, error) {
		return loadLinkedAggregates(ctx, targetBudgetID, viewerID, userToday)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.([]linkedAggRow), nil
}

// loadProfilesSF wraps loadProfiles. Multiple concurrent summaries on the
// same budget often request the same set of user ids (the budget's
// collaborators).
//
// PERF: pull individual profile rows from Redis where present; only the
// missing ones go to Postgres. Profile mutations invalidate via
// profileCacheKey so a name update is reflected in the next summary read.
// Singleflight collapses concurrent identical fetches; Redis collapses
// repeat fetches across requests.
func loadProfilesSF(ctx context.Context, ids map[uuid.UUID]struct{}) (map[uuid.UUID]*models.Profile, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]*models.Profile{}, nil
	}

	// First try Redis for each id individually. The wire format is the
	// JSON-encoded profile row; we keep that shape so the frontend
	// consumers (which already deserialize Profile) stay unchanged.
	out := make(map[uuid.UUID]*models.Profile, len(ids))
	missing := make(map[uuid.UUID]struct{}, len(ids))
	for id := range ids {
		if blob, hit := rediscache.Get(ctx, profileCacheKey(id)); hit {
			var p models.Profile
			if err := json.Unmarshal(blob, &p); err == nil {
				pCpy := p
				out[id] = &pCpy
				continue
			}
		}
		missing[id] = struct{}{}
	}
	if len(missing) == 0 {
		return out, nil
	}

	key := "summary:profiles:" + sortedUUIDsKey(missing)
	v, err, _ := summarySFGroup.Do(key, func() (any, error) {
		return loadProfiles(ctx, missing)
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return out, nil
	}
	loaded := v.(map[uuid.UUID]*models.Profile)
	for id, p := range loaded {
		out[id] = p
		// Cache for next time; profile rows are extremely read-mostly so
		// the 5-minute TTL plus invalidation-on-update keeps staleness
		// invisible.
		if blob, mErr := json.Marshal(p); mErr == nil {
			rediscache.Set(ctx, profileCacheKey(id), blob, profileCacheTTL)
		}
	}
	return out, nil
}

// sortedUUIDsKey produces a stable string key for a set of user ids.
// Used by loadProfilesSF so call sites with the same ids hash to the
// same singleflight bucket.
func sortedUUIDsKey(ids map[uuid.UUID]struct{}) string {
	if len(ids) == 0 {
		return ""
	}
	arr := make([]uuid.UUID, 0, len(ids))
	for id := range ids {
		arr = append(arr, id)
	}
	// Tiny insertion sort — len(arr) is bounded by the number of
	// collaborators on a budget (~6). Sorting in place avoids allocating
	// a sort.Sort interface.
	for i := 1; i < len(arr); i++ {
		key := arr[i]
		j := i - 1
		for j >= 0 && uuidLess(key, arr[j]) {
			arr[j+1] = arr[j]
			j--
		}
		arr[j+1] = key
	}
	// Pre-size: 36 chars per UUID + 1 separator each.
	b := make([]byte, 0, len(arr)*37)
	for i, u := range arr {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, u.String()...)
	}
	return string(b)
}

// uuidLess compares two UUIDs lexicographically by byte order. The
// canonical ordering is what consumers of sorted UUID lists expect.
func uuidLess(a, b uuid.UUID) bool {
	for i := 0; i < 16; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// trendsAggregateKey builds the singleflight key for a trends query. The
// kind tag distinguishes the (cutoff-bound vs full-history) SQL branches
// in GetBudgetTrends — they return different rowsets so they must not
// share a key.
func trendsAggregateKey(budgetID uuid.UUID, useCutoff bool) string {
	if useCutoff {
		return "trends:" + budgetID.String() + ":cutoff"
	}
	return "trends:" + budgetID.String() + ":full"
}

// resumeAggregateKey builds the singleflight key for the resume query.
// The today date string is part of the key because a day rollover can
// shift the period boundaries.
func resumeAggregateKey(budgetID uuid.UUID, userToday time.Time) string {
	return "resume:" + budgetID.String() + ":" + userToday.Format("2006-01-02")
}
