-- schema.sql — Portable database schema for the Financial Workspace.
-- Run against any PostgreSQL 13+ database to bootstrap all tables.
-- Usage: psql "postgresql://..." -f schema.sql

-- Enable UUID generation
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ─── profiles ────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS profiles (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE,
    full_name     TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    auth_provider TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_profiles_email ON profiles(email);

ALTER TABLE profiles DISABLE ROW LEVEL SECURITY;

-- ─── budgets ─────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS budgets (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id               UUID NOT NULL REFERENCES profiles(id),
    name                  TEXT NOT NULL,
    icon                  TEXT NOT NULL DEFAULT 'wallet',
    monthly_income        NUMERIC(18,2) NOT NULL DEFAULT 0,
    currency              TEXT NOT NULL DEFAULT 'USD',
    billing_period_months INTEGER NOT NULL DEFAULT 1,
    billing_cutoff_day    INTEGER NOT NULL DEFAULT 1,
    mode                  TEXT NOT NULL DEFAULT 'manual',
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_budgets_user_id ON budgets(user_id);

ALTER TABLE budgets DISABLE ROW LEVEL SECURITY;

-- ─── budget_categories (flat: max 50 per budget) ────────────────────────────

CREATE TABLE IF NOT EXISTS budget_categories (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    budget_id         UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    allocation_value  NUMERIC(18,2) NOT NULL DEFAULT 0,
    icon              TEXT NOT NULL DEFAULT '',
    sort_order        INTEGER NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_budget_categories_budget_id ON budget_categories(budget_id);
CREATE INDEX IF NOT EXISTS idx_budget_categories_budget_sort ON budget_categories(budget_id, sort_order);

ALTER TABLE budget_categories DISABLE ROW LEVEL SECURITY;

-- Enforce maximum of 50 categories per budget.
CREATE OR REPLACE FUNCTION enforce_budget_category_cap() RETURNS TRIGGER AS $$
BEGIN
  IF (SELECT COUNT(*) FROM budget_categories WHERE budget_id = NEW.budget_id) > 50 THEN
    RAISE EXCEPTION 'budget already has maximum 50 categories';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_enforce_budget_category_cap ON budget_categories;
CREATE TRIGGER trg_enforce_budget_category_cap
    AFTER INSERT ON budget_categories
    FOR EACH ROW EXECUTE FUNCTION enforce_budget_category_cap();

-- ─── budget_expenses ─────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS budget_expenses (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    budget_id       UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    category_id     UUID NOT NULL REFERENCES budget_categories(id) ON DELETE CASCADE,
    amount          NUMERIC(18,2) NOT NULL DEFAULT 0,
    description     TEXT NOT NULL DEFAULT '',
    expense_date    DATE NOT NULL DEFAULT CURRENT_DATE,
    created_by      UUID REFERENCES profiles(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_budget_expenses_budget_id ON budget_expenses(budget_id);
CREATE INDEX IF NOT EXISTS idx_budget_expenses_category_id ON budget_expenses(category_id);
CREATE INDEX IF NOT EXISTS idx_budget_expenses_expense_date ON budget_expenses(expense_date);

ALTER TABLE budget_expenses DISABLE ROW LEVEL SECURITY;

-- ─── budget_collaborators ────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS budget_collaborators (
    id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    budget_id UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    user_id   UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    role      TEXT NOT NULL DEFAULT 'collaborator',
    added_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(budget_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_budget_collaborators_budget_id ON budget_collaborators(budget_id);
CREATE INDEX IF NOT EXISTS idx_budget_collaborators_user_id ON budget_collaborators(user_id);

ALTER TABLE budget_collaborators DISABLE ROW LEVEL SECURITY;

-- ─── budget_invites ──────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS budget_invites (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    budget_id    UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    invite_token TEXT NOT NULL UNIQUE,
    created_by   UUID NOT NULL REFERENCES profiles(id),
    used_by      UUID REFERENCES profiles(id),
    used_at      TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_budget_invites_invite_token ON budget_invites(invite_token);
CREATE INDEX IF NOT EXISTS idx_budget_invites_budget_id ON budget_invites(budget_id);

ALTER TABLE budget_invites DISABLE ROW LEVEL SECURITY;

-- ─── user_sessions ──────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS user_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    token_hash      TEXT NOT NULL UNIQUE,
    ip_address      TEXT NOT NULL DEFAULT '',
    device_type     TEXT NOT NULL DEFAULT 'desktop',
    browser         TEXT NOT NULL DEFAULT '',
    os              TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_active_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_token_hash ON user_sessions(token_hash);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user_id ON user_sessions(user_id);

ALTER TABLE user_sessions DISABLE ROW LEVEL SECURITY;

-- ─── budget_links ───────────────────────────────────────────────────────────
-- Category-level links only: each row links a single source category into a
-- target budget. Section concepts have been removed.

CREATE TABLE IF NOT EXISTS budget_links (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_budget_id   UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    target_budget_id   UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    source_category_id UUID NOT NULL REFERENCES budget_categories(id) ON DELETE CASCADE,
    filter_mode        TEXT NOT NULL DEFAULT 'all',
    created_by         UUID REFERENCES profiles(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_filter_mode CHECK (filter_mode IN ('all', 'mine')),
    CONSTRAINT chk_different_budgets CHECK (source_budget_id != target_budget_id),
    CONSTRAINT budget_links_target_category_unique UNIQUE (target_budget_id, source_category_id)
);

CREATE INDEX IF NOT EXISTS idx_budget_links_source_budget ON budget_links(source_budget_id);
CREATE INDEX IF NOT EXISTS idx_budget_links_target_budget ON budget_links(target_budget_id);
CREATE INDEX IF NOT EXISTS idx_budget_links_source_category ON budget_links(source_category_id);

ALTER TABLE budget_links DISABLE ROW LEVEL SECURITY;

-- ─── display_orders (per-user visual ordering) ─────────────────────────────

CREATE TABLE IF NOT EXISTS display_orders (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    scope_key   TEXT NOT NULL,
    ordered_ids JSONB NOT NULL DEFAULT '[]',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(user_id, scope_key)
);

CREATE INDEX IF NOT EXISTS idx_display_orders_user_id ON display_orders(user_id);

ALTER TABLE display_orders DISABLE ROW LEVEL SECURITY;

-- ─── composite indexes for common query patterns ─────────────────────────────
CREATE INDEX IF NOT EXISTS idx_budget_expenses_budget_date ON budget_expenses(budget_id, expense_date);
CREATE INDEX IF NOT EXISTS idx_budget_expenses_budget_category ON budget_expenses(budget_id, category_id);
CREATE INDEX IF NOT EXISTS idx_budget_collaborators_budget_user ON budget_collaborators(budget_id, user_id);
