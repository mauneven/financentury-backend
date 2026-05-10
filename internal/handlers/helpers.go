package handlers

import (
	"context"

	"github.com/google/uuid"

	"github.com/the-financial-workspace/backend/internal/cache"
)

// invalidateBudget drops every cached summary / trends / resume entry for a
// budget.
//
// PERF: Mutation handlers (expense / category / link / collab CRUD) call this
// after a successful write so the next read rebuilds from fresh DB state.
// Without this call, the 30s TTL in the summary cache could serve stale data
// across a write on a high-frequency update (e.g. rapid expense entry).
//
// Delegates to the real cache.InvalidateBudget. This wrapper exists so every
// handler has a single obvious invalidation point — search for
// `invalidateBudget(` to find every write-site that touches the cache.
func invalidateBudget(budgetID uuid.UUID) {
	cache.InvalidateBudget(budgetID)
}

// invalidateLinkedTargets drops cached entries for every budget that has a
// link pointing at sourceBudgetID. Called alongside invalidateBudget on every
// category / expense mutation because a target budget's summary embeds the
// source budget's linked_categories view — so a change on the source must
// purge caches on all dependents too.
//
// Best-effort: a lookup error is swallowed (the TTL will catch the stale
// entry within 30 seconds). Skipping invalidation on error is strictly safer
// than refusing the write.
func invalidateLinkedTargets(sourceBudgetID uuid.UUID) {
	targetIDs, err := fetchTargetBudgetIDs(context.Background(), sourceBudgetID)
	if err != nil || len(targetIDs) == 0 {
		return
	}
	for _, tid := range targetIDs {
		cache.InvalidateBudget(tid)
	}
}
