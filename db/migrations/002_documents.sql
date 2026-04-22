--
-- Migration 002: PDF document page extraction
-- Adds documents and document_pages tables.
-- The existing users/files tables are unchanged.
--

-- documents
-- Tracks every uploaded PDF and its processing lifecycle.
CREATE TABLE IF NOT EXISTS documents (
    id                  UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id             UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    filename            TEXT        NOT NULL,
    original_minio_path TEXT        NOT NULL,
    sha256              TEXT        NOT NULL,

    -- processing → ready | failed
    status              TEXT        NOT NULL DEFAULT 'processing',
    total_pages         INT,
    error_message       TEXT,

    size_bytes          BIGINT      NOT NULL DEFAULT 0,

    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_documents_user_status
    ON documents(user_id, status);

CREATE INDEX IF NOT EXISTS idx_documents_sha256
    ON documents(sha256);

-- document_pages
-- Each row represents one extracted page stored as a standalone PDF in MinIO.
CREATE TABLE IF NOT EXISTS document_pages (
    id              UUID    PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id     UUID    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    page_number     INT     NOT NULL,
    minio_path      TEXT    NOT NULL,                 -- documents/{doc_id}/pages/{n}.pdf
    size_bytes      BIGINT  NOT NULL DEFAULT 0,

    UNIQUE(document_id, page_number)
);

CREATE INDEX IF NOT EXISTS idx_pages_document_page
    ON document_pages(document_id, page_number);

-- webhook subscriptions
-- External services register a URL to be notified when a document finishes processing.
CREATE TABLE IF NOT EXISTS webhook_subscriptions (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    document_id     UUID        REFERENCES documents(id) ON DELETE CASCADE,
    url             TEXT        NOT NULL,
    secret          TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_webhooks_document
    ON webhook_subscriptions(document_id);
