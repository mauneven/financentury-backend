-- 003_attest_keys.sql
--
-- App Attest (iOS) + Play Integrity (Android) key registration table.
-- Stores the per-app-instance hardware-backed public key used to verify
-- subsequent attestation assertions on the auth surface.
--
-- Apply on a live database:
--   psql "postgresql://..." -f migrations/003_attest_keys.sql
--
-- The migration is idempotent: every CREATE uses IF NOT EXISTS.
--
-- ─── Why ────────────────────────────────────────────────────────────────────
--
-- Per-request App Attest verification needs (a) a registered ECDSA P-256
-- public key keyed by an opaque key_id, and (b) a monotonically-increasing
-- signCount to detect signature replay. We persist both per app instance.
--
-- The user_id FK is NULLABLE because attestation can be performed in two
-- modes:
--   1. Pre-auth (anonymous): client registers the key on first launch,
--      before signing in. user_id stays NULL.
--   2. Post-auth: client re-registers when the signed-in user changes so
--      revoking the user invalidates the device-level trust. user_id
--      points to profiles(id).
--
-- platform column allows future Android Hardware Attestation rows (Play
-- Integrity exposes a similar per-device key concept in its "classic"
-- variant). For the standard variant we still create a row to track the
-- nonce-issuing surface per package_name.

CREATE TABLE IF NOT EXISTS attest_keys (
    id           UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID         NULL REFERENCES profiles(id) ON DELETE CASCADE,
    platform     TEXT         NOT NULL CHECK (platform IN ('ios', 'android')),
    key_id       TEXT         NOT NULL,
    public_key   BYTEA        NULL,       -- iOS attestation public key (DER, PKIX).
    counter      BIGINT       NOT NULL DEFAULT 0,
    last_seen_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    UNIQUE (platform, key_id)
);

-- Only index user_id when populated. The vast majority of pre-auth
-- registrations carry NULL user_id; a partial index keeps the b-tree
-- compact and avoids indexing the dominant NULL bucket.
CREATE INDEX IF NOT EXISTS idx_attest_keys_user
    ON attest_keys(user_id) WHERE user_id IS NOT NULL;

ALTER TABLE attest_keys DISABLE ROW LEVEL SECURITY;

-- ─── attest_challenges ──────────────────────────────────────────────────────
--
-- Single-use challenges issued by GET /api/attest/challenge. The captcha
-- middleware consumes the challenge during App Attest assertion verification
-- so the (assertion, client_data_hash) pair cannot be replayed.
--
-- Rows are short-lived (5-minute TTL); a cron / on-write sweep keeps the
-- table from accumulating. We don't run a strict TIMESTAMPTZ NOT NULL
-- DEFAULT now() index because the population is small and reads/writes are
-- by-PK only.

CREATE TABLE IF NOT EXISTS attest_challenges (
    challenge    BYTEA       PRIMARY KEY,
    issued_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_attest_challenges_expires
    ON attest_challenges(expires_at);

ALTER TABLE attest_challenges DISABLE ROW LEVEL SECURITY;
