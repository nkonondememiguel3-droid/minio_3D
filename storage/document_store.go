package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// DocumentStore handles all PostgreSQL operations for documents and pages.
type DocumentStore struct {
	db *sqlx.DB
}

// NewDocumentStore wraps an open sqlx.DB connection.
func NewDocumentStore(db *sqlx.DB) *DocumentStore {
	return &DocumentStore{db: db}
}

// document operations

// CreateDocument inserts a new document record with status=processing.
func (s *DocumentStore) CreateDocument(userID, filename, minioPath, sha256 string, sizeBytes int64) (*Document, error) {
	tx, err := s.db.Beginx()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	const q = `
		INSERT INTO documents (user_id, filename, original_minio_path, sha256, size_bytes)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, filename, original_minio_path, sha256,
		          status, total_pages, error_message, size_bytes, created_at, updated_at`

	var d Document
	if err := tx.QueryRowx(q, userID, filename, minioPath, sha256, sizeBytes).StructScan(&d); err != nil {
		return nil, fmt.Errorf("create document: %w", err)
	}

	// Update storage quota usage atomically with the document insert.
	const updateUsage = `
		UPDATE users
		SET storage_bytes_used = storage_bytes_used + $1
		WHERE id = $2`
	if _, err := tx.Exec(updateUsage, sizeBytes, userID); err != nil {
		return nil, fmt.Errorf("update storage usage: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return &d, nil
}

// GetDocument retrieves a document by ID, asserting ownership.
func (s *DocumentStore) GetDocument(id, userID string) (*Document, error) {
	const q = `
		SELECT id, user_id, filename, original_minio_path, sha256,
		       status, total_pages, error_message, size_bytes, created_at, updated_at
		FROM documents
		WHERE id = $1 AND user_id = $2`

	var d Document
	if err := s.db.QueryRowx(q, id, userID).StructScan(&d); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get document: %w", err)
	}
	return &d, nil
}

// GetDocumentByID retrieves a document by ID only (used by the worker, which has no user context).
func (s *DocumentStore) GetDocumentByID(id string) (*Document, error) {
	const q = `
		SELECT id, user_id, filename, original_minio_path, sha256,
		       status, total_pages, error_message, size_bytes, created_at, updated_at
		FROM documents WHERE id = $1`

	var d Document
	if err := s.db.QueryRowx(q, id).StructScan(&d); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get document by id: %w", err)
	}
	return &d, nil
}

// ListDocuments returns all documents for a user, newest first.
func (s *DocumentStore) ListDocuments(userID string) ([]Document, error) {
	const q = `
		SELECT id, user_id, filename, original_minio_path, sha256,
		       status, total_pages, error_message, size_bytes, created_at, updated_at
		FROM documents
		WHERE user_id = $1
		ORDER BY created_at DESC`

	var docs []Document
	if err := s.db.Select(&docs, q, userID); err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	return docs, nil
}

// MarkReady sets status=ready and stores the total page count.
func (s *DocumentStore) MarkReady(docID string, totalPages int) error {
	const q = `
		UPDATE documents
		SET status = 'ready', total_pages = $1, updated_at = NOW()
		WHERE id = $2`

	if _, err := s.db.Exec(q, totalPages, docID); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	return nil
}

// MarkFailed sets status=failed and stores the error message.
func (s *DocumentStore) MarkFailed(docID, errMsg string) error {
	const q = `
		UPDATE documents
		SET status = 'failed', error_message = $1, updated_at = NOW()
		WHERE id = $2`

	if _, err := s.db.Exec(q, errMsg, docID); err != nil {
		return fmt.Errorf("mark failed: %w", err)
	}
	return nil
}

// ─── page operations ──────────────────────────────────────────────────────────

// SavePage inserts a single extracted page record.
func (s *DocumentStore) SavePage(docID string, pageNumber int, minioPath string, sizeBytes int64) (*DocumentPage, error) {
	const q = `
		INSERT INTO document_pages (document_id, page_number, minio_path, size_bytes)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (document_id, page_number) DO UPDATE
		    SET minio_path = EXCLUDED.minio_path, size_bytes = EXCLUDED.size_bytes
		RETURNING id, document_id, page_number, minio_path, size_bytes`

	var p DocumentPage
	if err := s.db.QueryRowx(q, docID, pageNumber, minioPath, sizeBytes).StructScan(&p); err != nil {
		return nil, fmt.Errorf("save page: %w", err)
	}
	return &p, nil
}

// GetPage retrieves a single page record by document and page number.
func (s *DocumentStore) GetPage(docID string, pageNumber int) (*DocumentPage, error) {
	const q = `
		SELECT id, document_id, page_number, minio_path, size_bytes
		FROM document_pages
		WHERE document_id = $1 AND page_number = $2`

	var p DocumentPage
	if err := s.db.QueryRowx(q, docID, pageNumber).StructScan(&p); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get page: %w", err)
	}
	return &p, nil
}

// GetPageRange returns pages between start and end (inclusive, 1-indexed).
func (s *DocumentStore) GetPageRange(docID string, start, end int) ([]DocumentPage, error) {
	const q = `
		SELECT id, document_id, page_number, minio_path, size_bytes
		FROM document_pages
		WHERE document_id = $1 AND page_number BETWEEN $2 AND $3
		ORDER BY page_number ASC`

	var pages []DocumentPage
	if err := s.db.Select(&pages, q, docID, start, end); err != nil {
		return nil, fmt.Errorf("get page range: %w", err)
	}
	return pages, nil
}

// ListPages returns all extracted pages for a document, ordered by page number.
func (s *DocumentStore) ListPages(docID string) ([]DocumentPage, error) {
	const q = `
		SELECT id, document_id, page_number, minio_path, size_bytes
		FROM document_pages
		WHERE document_id = $1
		ORDER BY page_number ASC`

	var pages []DocumentPage
	if err := s.db.Select(&pages, q, docID); err != nil {
		return nil, fmt.Errorf("list pages: %w", err)
	}
	return pages, nil
}

// ─── webhook operations ───────────────────────────────────────────────────────

// CreateWebhook registers a webhook URL for a document.
func (s *DocumentStore) CreateWebhook(userID, docID, url, secret string) (*WebhookSubscription, error) {
	const q = `
		INSERT INTO webhook_subscriptions (user_id, document_id, url, secret)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, document_id, url, secret, created_at`

	var w WebhookSubscription
	if err := s.db.QueryRowx(q, userID, docID, url, secret).StructScan(&w); err != nil {
		return nil, fmt.Errorf("create webhook: %w", err)
	}
	return &w, nil
}

// GetWebhooksForDocument returns all webhook subscriptions for a document.
func (s *DocumentStore) GetWebhooksForDocument(docID string) ([]WebhookSubscription, error) {
	const q = `
		SELECT id, user_id, document_id, url, secret, created_at
		FROM webhook_subscriptions
		WHERE document_id = $1`

	var hooks []WebhookSubscription
	if err := s.db.Select(&hooks, q, docID); err != nil {
		return nil, fmt.Errorf("get webhooks: %w", err)
	}
	return hooks, nil
}

// UpdateOriginalPath patches the original_minio_path after the file is uploaded.
func (s *DocumentStore) UpdateOriginalPath(docID, path string) error {
	const q = `UPDATE documents SET original_minio_path = $1, updated_at = NOW() WHERE id = $2`
	if _, err := s.db.Exec(q, path, docID); err != nil {
		return fmt.Errorf("update original path: %w", err)
	}
	return nil
}

// DeleteDocument removes a document and all its pages (CASCADE handles DB rows).
// The caller is responsible for deleting the MinIO objects separately.
func (s *DocumentStore) DeleteDocument(id, userID string) (*Document, error) {
	const q = `
		DELETE FROM documents
		WHERE id = $1 AND user_id = $2
		RETURNING id, user_id, filename, original_minio_path, sha256,
		          status, total_pages, error_message, size_bytes, created_at, updated_at`

	var d Document
	if err := s.db.QueryRowx(q, id, userID).StructScan(&d); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("delete document: %w", err)
	}
	return &d, nil
}

// FailStuckDocuments marks documents that have been in "processing" longer
// than the cutoff as "failed". Returns the number of rows updated.
// Called by the watchdog on a regular interval.
func (s *DocumentStore) FailStuckDocuments(cutoff time.Time) (int, error) {
	const q = `
		UPDATE documents
		SET status        = 'failed',
		    error_message = 'processing timeout: worker did not complete in time',
		    updated_at    = NOW()
		WHERE status     = 'processing'
		  AND created_at < $1`

	res, err := s.db.Exec(q, cutoff)
	if err != nil {
		return 0, fmt.Errorf("fail stuck documents: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
