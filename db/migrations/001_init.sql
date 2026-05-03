-- ─────────────────────────────────────────────────────────────────────────────
-- Migration 001 — initial schema
-- Includes gap fixes: ref_count (shared-object safety) + per-user quotas
-- ─────────────────────────────────────────────────────────────────────────────

-- Enable UUID generation
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- ─── users ───────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS users (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email               TEXT        NOT NULL UNIQUE,
    password_hash       TEXT        NOT NULL,

    -- Quota tracking (gap fix)
    storage_bytes_used  BIGINT      NOT NULL DEFAULT 0,
    storage_quota_bytes BIGINT      NOT NULL DEFAULT 10737418240, -- 10 GB

    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ─── files ───────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS files (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    object_key   TEXT        NOT NULL,
    sha256       TEXT        NOT NULL,
    size         BIGINT      NOT NULL,
    content_type TEXT        NOT NULL,

    -- Reference counting (gap fix): counts how many file rows share this object_key.
    -- The backing object in storage is only deleted when ref_count reaches 0.
    ref_count    INT         NOT NULL DEFAULT 1,

    original_name TEXT       NOT NULL DEFAULT '',

    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ          -- soft delete; NULL = active
);

-- Global uniqueness on sha256 enforces deduplication across all users.
CREATE INDEX IF NOT EXISTS idx_files_sha256
    ON files(sha256)
    WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_files_user_id
    ON files(user_id);

CREATE INDEX IF NOT EXISTS idx_files_active
    ON files(user_id, deleted_at)
    WHERE deleted_at IS NULL;
