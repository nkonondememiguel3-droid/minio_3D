-- ─────────────────────────────────────────────────────────────────────────────
-- Migration 003 — API key authentication for machine-to-machine access
-- ─────────────────────────────────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS api_keys (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT        NOT NULL,                  -- human label e.g. "production backend"
    key_hash     TEXT        NOT NULL UNIQUE,           -- SHA-256 of the raw key — never store plaintext
    prefix       TEXT        NOT NULL,                  -- first 8 chars of raw key for display
    last_used_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ                            -- NULL = never expires
);

CREATE INDEX IF NOT EXISTS idx_api_keys_user
    ON api_keys(user_id);

CREATE INDEX IF NOT EXISTS idx_api_keys_hash
    ON api_keys(key_hash);
