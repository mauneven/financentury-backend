-- 002_index_audit.sql
--
-- Index audit + targeted optimizations for the dominant access patterns
-- across summary, expenses, budgets, links, sessions, invites, and auth.
--
-- Apply on a live database (no transaction wrapper — CREATE INDEX
-- CONCURRENTLY cannot run inside a transaction block):
--   psql "postgresql://..." -f migrations/002_index_audit.sql
--
-- The script is idempotent: every CREATE/DROP uses IF [NOT] EXISTS, and
-- CONCURRENTLY ensures no exclusive locks are taken on the underlying tables.
-- A partial CONCURRENTLY build that fails mid-flight leaves an INVALID index
-- behind; the IF NOT EXISTS clause keeps re-runs safe — drop the invalid
-- index manually and re-run if that ever happens (DROP INDEX CONCURRENTLY
-- IF EXISTS <name>;).
--
-- ─── Why these changes ──────────────────────────────────────────────────────
--
-- ADDS (6):
--
-- 1. idx_user_sessions_token_hash_covering
--    Path: middleware/auth.go Protected() — runs on EVERY authenticated
--    request when the session cache misses (5-min TTL, hot on cold start
--    and post-deploy traffic).
--    Query: SELECT id, user_id::text, revoked_at, last_active_at
--           FROM user_sessions WHERE token_hash = $1
--    The existing UNIQUE constraint creates a btree on (token_hash); the
--    new INCLUDE-covering index lets the planner serve the SELECT as an
--    index-only scan — no heap fetch, no visibility-map miss in steady
--    state. Saves ~1 random heap I/O per cache miss. The implicit unique
--    index stays for constraint enforcement; the planner picks whichever
--    is cheaper per query.
--
-- 2. idx_user_sessions_user_active (PARTIAL)
--    Path: handlers/sessions.go ListSessions
--    Query: WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > NOW()
--           ORDER BY last_active_at DESC
--    Partial on `revoked_at IS NULL` because revoked sessions are immutable
--    history — they never satisfy this predicate again. On a long-running
--    deployment where revoked rows accumulate over months, this can shrink
--    the index by 5-10x and turn the ORDER BY into an index-only walk.
--
-- 3. idx_budget_expenses_created_by (PARTIAL)
--    Path: handlers/auth.go DeleteAccount
--    Query: UPDATE budget_expenses SET created_by = NULL WHERE created_by = $1
--    Without this, the UPDATE sequentially scans the entire expense table.
--    On the worst case of 7 budgets * 3000 expenses per user (= 21k rows
--    per affected user, scanned across the entire global expenses table)
--    that's a measurable hold on the connection. Partial on
--    `created_by IS NOT NULL` so the index only covers actually-attributed
--    rows (NULL `created_by` from legacy/imported data is excluded).
--
-- 4. idx_budget_links_created_by (PARTIAL)
--    Path: handlers/budgets.go DeleteBudget cleanup,
--          handlers/collaborators.go RemoveCollaborator cleanup,
--          handlers/auth.go DeleteAccount.
--    Query: WHERE created_by = $1 AND (source_budget_id = $2
--                                       OR target_budget_id = $2)
--    The OR on budget id is already satisfied by existing budget-id
--    indexes; this index makes the `created_by` filter selective so the
--    planner can intersect them with a BitmapAnd. Partial on IS NOT NULL
--    because the FK is ON DELETE SET NULL — every NULL row is a tombstone
--    that we never want to lookup by creator.
--
-- 5. idx_budget_invites_created_by
--    Path: handlers/auth.go DeleteAccount.
--    Query: DELETE FROM budget_invites WHERE created_by = $1
--    Without this, account deletion sequentially scans the entire invites
--    table. created_by is NOT NULL (declared so in the schema) so a plain
--    btree is the cheapest option.
--
-- 6. idx_budget_invites_used_by (PARTIAL on used_by IS NOT NULL)
--    Path: handlers/auth.go DeleteAccount.
--    Query: UPDATE budget_invites SET used_by = NULL WHERE used_by = $1
--    Partial because the column is nullable and most invites are unused at
--    any moment in time — the partial keeps the index small (only consumed
--    invites are indexed).
--
-- DROPS (8): redundant indexes either duplicating UNIQUE-constraint indexes
-- or fully covered by composite indexes already in place.
--
-- ── duplicates of UNIQUE-constraint implicit indexes ──
--   idx_profiles_email                   (UNIQUE on email)
--   idx_user_sessions_token_hash         (UNIQUE on token_hash; replaced by
--                                         covering INCLUDE index above)
--   idx_budget_invites_invite_token      (UNIQUE on invite_token)
--   idx_budget_collaborators_budget_user (UNIQUE on (budget_id, user_id))
--   idx_display_orders_user_id           (UNIQUE on (user_id, scope_key) —
--                                         leading column suffices)
--
-- ── covered by other composites ──
--   idx_budget_categories_budget_id      (covered by
--                                         idx_budget_categories_budget_sort)
--   idx_budget_expenses_budget_id        (covered by
--                                         idx_budget_expenses_budget_date and
--                                         idx_budget_expenses_budget_category)
--   idx_budget_collaborators_budget_id   (the UNIQUE-constraint implicit
--                                         index leads with budget_id and
--                                         suffices for budget-only lookups)
--
-- Net effect: -8 indexes + 6 indexes = -2 indexes overall, plus a covering
-- index on the single hottest query in the codebase (per-request session
-- validation).

-- ─── ADDITIONS ──────────────────────────────────────────────────────────────

-- Covering index for the per-request session lookup. INCLUDE columns are
-- payload-only (not part of the search key), so this complements rather
-- than duplicates the UNIQUE constraint's implicit btree on token_hash.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_sessions_token_hash_covering
    ON user_sessions(token_hash)
    INCLUDE (id, user_id, revoked_at, last_active_at);

-- Active-only sessions per user, ordered by last activity for ListSessions.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_sessions_user_active
    ON user_sessions(user_id, last_active_at DESC)
    WHERE revoked_at IS NULL;

-- Per-creator expense lookup, used by account deletion to NULL out FKs.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_budget_expenses_created_by
    ON budget_expenses(created_by)
    WHERE created_by IS NOT NULL;

-- Per-creator link lookup, used by budget delete, collaborator removal,
-- and account deletion.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_budget_links_created_by
    ON budget_links(created_by)
    WHERE created_by IS NOT NULL;

-- Invite lookups: account deletion clears used_by/created_by FKs; AcceptInvite
-- additionally filters on used_by IS NULL via the PK so no separate index for
-- that path is needed.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_budget_invites_created_by
    ON budget_invites(created_by);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_budget_invites_used_by
    ON budget_invites(used_by)
    WHERE used_by IS NOT NULL;

-- ─── REMOVALS ───────────────────────────────────────────────────────────────

-- Duplicates of UNIQUE-constraint indexes (the constraints create an
-- implicit btree that already serves equality lookups).
DROP INDEX CONCURRENTLY IF EXISTS idx_profiles_email;
DROP INDEX CONCURRENTLY IF EXISTS idx_user_sessions_token_hash;
DROP INDEX CONCURRENTLY IF EXISTS idx_budget_invites_invite_token;
DROP INDEX CONCURRENTLY IF EXISTS idx_budget_collaborators_budget_user;
DROP INDEX CONCURRENTLY IF EXISTS idx_display_orders_user_id;

-- Single-column FK indexes already covered by composite indexes.
DROP INDEX CONCURRENTLY IF EXISTS idx_budget_categories_budget_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_budget_expenses_budget_id;
DROP INDEX CONCURRENTLY IF EXISTS idx_budget_collaborators_budget_id;
