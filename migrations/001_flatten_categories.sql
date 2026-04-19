-- 001_flatten_categories.sql
--
-- Collapses the Budget -> Section (budget_categories) -> Category
-- (budget_subcategories) hierarchy into a single flat Budget -> Category
-- level, capped at 50 categories per budget. Existing subcategory IDs are
-- preserved so existing expenses, display orders and links keep working.
--
-- Apply with:
--   psql "postgresql://..." -f migrations/001_flatten_categories.sql
--
-- The script is idempotent per-run via a single transaction; re-running after
-- success will fail because the legacy tables no longer exist — which is the
-- desired signal.

BEGIN;

-- 1. Copy subcategories (old Category) into a flat budget_categories_new
-- table, preserving IDs so expenses still point at valid rows.
CREATE TABLE budget_categories_new (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    budget_id         UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    allocation_value  NUMERIC(18,2) NOT NULL DEFAULT 0,
    icon              TEXT NOT NULL DEFAULT '',
    sort_order        INTEGER NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO budget_categories_new (id, budget_id, name, allocation_value, icon, sort_order, created_at)
SELECT sub.id,
       sec.budget_id,
       sub.name,
       sub.allocation_value,
       sub.icon,
       sec.sort_order * 1000 + sub.sort_order,
       sub.created_at
FROM budget_subcategories sub
JOIN budget_categories sec ON sec.id = sub.category_id;

-- 2. Enforce the 50-per-budget cap by dropping overflow categories (oldest
-- kept, sorted by their flattened sort_order then created_at).
WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (PARTITION BY budget_id ORDER BY sort_order, created_at) AS rn
    FROM budget_categories_new
)
DELETE FROM budget_categories_new
WHERE id IN (SELECT id FROM ranked WHERE rn > 50);

-- 3. Expand section-level links into one link per category. Section-only
-- links (source_category_id IS NULL) are fanned out to every subcategory of
-- the referenced section; those original section rows are then removed.
INSERT INTO budget_links (
    id, source_budget_id, target_budget_id, source_section_id,
    source_category_id, target_section_id, filter_mode, created_by, created_at
)
SELECT gen_random_uuid(),
       bl.source_budget_id,
       bl.target_budget_id,
       bl.source_section_id,
       sub.id,
       NULL,
       bl.filter_mode,
       bl.created_by,
       bl.created_at
FROM budget_links bl
JOIN budget_subcategories sub ON sub.category_id = bl.source_section_id
WHERE bl.source_category_id IS NULL;

DELETE FROM budget_links WHERE source_category_id IS NULL;

-- 4. Drop the now-obsolete foreign keys so we can rename tables and columns
-- without the constraints fighting us.
ALTER TABLE budget_links  DROP CONSTRAINT IF EXISTS budget_links_source_section_id_fkey;
ALTER TABLE budget_links  DROP CONSTRAINT IF EXISTS budget_links_source_category_id_fkey;
ALTER TABLE budget_links  DROP CONSTRAINT IF EXISTS budget_links_target_section_id_fkey;
ALTER TABLE budget_expenses DROP CONSTRAINT IF EXISTS budget_expenses_subcategory_id_fkey;

-- 5. Swap tables: legacy names get a "_legacy_drop" suffix, and the flat
-- replacement takes over the canonical name.
ALTER TABLE budget_categories    RENAME TO budget_sections_legacy_drop;
ALTER TABLE budget_subcategories RENAME TO budget_subcategories_legacy_drop;
ALTER TABLE budget_categories_new RENAME TO budget_categories;

-- 6. Rename the expense column to match the flat model and re-add the FK.
ALTER TABLE budget_expenses RENAME COLUMN subcategory_id TO category_id;
ALTER TABLE budget_expenses ADD CONSTRAINT budget_expenses_category_id_fkey
    FOREIGN KEY (category_id) REFERENCES budget_categories(id) ON DELETE CASCADE;

-- 7. Rewrite budget_links schema: drop section columns, make source_category_id
-- required, and re-add the single-category uniqueness constraint.
ALTER TABLE budget_links DROP COLUMN source_section_id;
ALTER TABLE budget_links DROP COLUMN target_section_id;
ALTER TABLE budget_links ALTER COLUMN source_category_id SET NOT NULL;
ALTER TABLE budget_links ADD CONSTRAINT budget_links_source_category_id_fkey
    FOREIGN KEY (source_category_id) REFERENCES budget_categories(id) ON DELETE CASCADE;
ALTER TABLE budget_links DROP CONSTRAINT IF EXISTS budget_links_target_budget_id_source_section_id_source_cate_key;
ALTER TABLE budget_links ADD CONSTRAINT budget_links_target_category_unique
    UNIQUE (target_budget_id, source_category_id);

-- 8. Rebuild indexes around the new columns/tables.
CREATE INDEX IF NOT EXISTS idx_budget_categories_budget_id   ON budget_categories(budget_id);
CREATE INDEX IF NOT EXISTS idx_budget_categories_budget_sort ON budget_categories(budget_id, sort_order);
DROP INDEX IF EXISTS idx_budget_expenses_subcategory_id;
CREATE INDEX IF NOT EXISTS idx_budget_expenses_category_id ON budget_expenses(category_id);
DROP INDEX IF EXISTS idx_budget_expenses_budget_subcategory;
CREATE INDEX IF NOT EXISTS idx_budget_expenses_budget_category ON budget_expenses(budget_id, category_id);
DROP INDEX IF EXISTS idx_budget_links_source_section;
CREATE INDEX IF NOT EXISTS idx_budget_links_source_category ON budget_links(source_category_id);

-- 9. Prune stale per-user display-order scope keys that referenced sections.
DELETE FROM display_orders
 WHERE scope_key LIKE 'budget-%-section-%-categories'
    OR scope_key LIKE 'section:%:categories'
    OR scope_key LIKE 'budget-%-sections'
    OR scope_key LIKE 'budget:%:sections';

-- 10. Drop the legacy tables now that nothing references them.
DROP TABLE budget_sections_legacy_drop;
DROP TABLE budget_subcategories_legacy_drop;

-- 11. Install the 50-category cap trigger.
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

COMMIT;
