package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("not found")

// ErrQuotaExceeded is returned when an upload would push the user over their quota.
var ErrQuotaExceeded = errors.New("storage quota exceeded")

// MetadataStore handles all PostgreSQL operations for users and files.
type MetadataStore struct {
	db *sqlx.DB
}

// NewMetadataStore wraps an open sqlx.DB connection.
func NewMetadataStore(db *sqlx.DB) *MetadataStore {
	return &MetadataStore{db: db}
}

// ─── user operations ─────────────────────────────────────────────────────────

// CreateUser inserts a new user and returns the full record.
func (m *MetadataStore) CreateUser(email, passwordHash string, quotaBytes int64) (*User, error) {
	const q = `
		INSERT INTO users (email, password_hash, storage_quota_bytes)
		VALUES ($1, $2, $3)
		RETURNING id, email, password_hash, storage_bytes_used, storage_quota_bytes, created_at`

	var u User
	if err := m.db.QueryRowx(q, email, passwordHash, quotaBytes).StructScan(&u); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return &u, nil
}

// GetUserByEmail looks up a user by email address.
func (m *MetadataStore) GetUserByEmail(email string) (*User, error) {
	const q = `SELECT id, email, password_hash, storage_bytes_used, storage_quota_bytes, created_at
	           FROM users WHERE email = $1`

	var u User
	if err := m.db.QueryRowx(q, email).StructScan(&u); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return &u, nil
}

// GetUserByID looks up a user by primary key.
func (m *MetadataStore) GetUserByID(id string) (*User, error) {
	const q = `SELECT id, email, password_hash, storage_bytes_used, storage_quota_bytes, created_at
	           FROM users WHERE id = $1`

	var u User
	if err := m.db.QueryRowx(q, id).StructScan(&u); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return &u, nil
}

// ─── file operations ─────────────────────────────────────────────────────────

// GetByHash returns the active file record with the given SHA-256 hash, if any.
func (m *MetadataStore) GetByHash(hash string) (*FileMeta, error) {
	const q = `
		SELECT id, user_id, object_key, sha256, size, content_type, ref_count, original_name, created_at, deleted_at
		FROM files
		WHERE sha256 = $1 AND deleted_at IS NULL
		LIMIT 1`

	var f FileMeta
	if err := m.db.QueryRowx(q, hash).StructScan(&f); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get by hash: %w", err)
	}
	return &f, nil
}

// GetByID returns the file record with the given ID belonging to userID.
func (m *MetadataStore) GetByID(id, userID string) (*FileMeta, error) {
	const q = `
		SELECT id, user_id, object_key, sha256, size, content_type, ref_count, original_name, created_at, deleted_at
		FROM files
		WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`

	var f FileMeta
	if err := m.db.QueryRowx(q, id, userID).StructScan(&f); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get by id: %w", err)
	}
	return &f, nil
}

// ListByUser returns all active files for a given user, newest first.
func (m *MetadataStore) ListByUser(userID string) ([]FileMeta, error) {
	const q = `
		SELECT id, user_id, object_key, sha256, size, content_type, ref_count, original_name, created_at, deleted_at
		FROM files
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`

	var files []FileMeta
	if err := m.db.Select(&files, q, userID); err != nil {
		return nil, fmt.Errorf("list by user: %w", err)
	}
	return files, nil
}

// SaveResult is returned by Save so the caller knows whether the file was
// freshly uploaded or resolved from an existing dedup hit.
type SaveResult struct {
	File      FileMeta
	Duplicate bool // true = another record already held this sha256
}

// Save inserts a new file record inside a transaction that also:
//   - enforces the user quota (gap fix 2)
//   - increments ref_count on the canonical sha256 row (gap fix 1)
//   - updates storage_bytes_used on the user
//
// If a file with the same sha256 already exists (dedup hit), Save creates a
// new file row for this user pointing to the same object_key and returns
// Duplicate=true. No storage write has occurred.
func (m *MetadataStore) Save(userID, objectKey, sha256Hash, contentType, originalName string, size int64) (*SaveResult, error) {
	tx, err := m.db.Beginx()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 1. Lock the user row so concurrent uploads don't race on quota.
	const lockUser = `SELECT storage_bytes_used, storage_quota_bytes FROM users WHERE id = $1 FOR UPDATE`
	var bytesUsed, quota int64
	if err := tx.QueryRow(lockUser, userID).Scan(&bytesUsed, &quota); err != nil {
		return nil, fmt.Errorf("lock user: %w", err)
	}

	// 2. Quota check (gap fix 2).
	if bytesUsed+size > quota {
		return nil, ErrQuotaExceeded
	}

	// 3. Check for an existing canonical row with the same sha256.
	var existing *FileMeta
	const findCanonical = `
		SELECT id, user_id, object_key, sha256, size, content_type, ref_count, original_name, created_at, deleted_at
		FROM files WHERE sha256 = $1 AND deleted_at IS NULL LIMIT 1`

	var canon FileMeta
	err = tx.QueryRowx(findCanonical, sha256Hash).StructScan(&canon)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("find canonical: %w", err)
	}
	if err == nil {
		existing = &canon
	}

	var newFile FileMeta

	if existing != nil {
		// ── Dedup hit ────────────────────────────────────────────────────────
		// Increment ref_count on the canonical row (gap fix 1).
		const incrRef = `UPDATE files SET ref_count = ref_count + 1 WHERE id = $1`
		if _, err := tx.Exec(incrRef, existing.ID); err != nil {
			return nil, fmt.Errorf("increment ref_count: %w", err)
		}

		// Insert a new file row for this user pointing to the same object.
		const insertDup = `
			INSERT INTO files (user_id, object_key, sha256, size, content_type, ref_count, original_name)
			VALUES ($1, $2, $3, $4, $5, 1, $6)
			RETURNING id, user_id, object_key, sha256, size, content_type, ref_count, original_name, created_at, deleted_at`

		if err := tx.QueryRowx(insertDup, userID, existing.ObjectKey, sha256Hash, size, contentType, originalName).
			StructScan(&newFile); err != nil {
			return nil, fmt.Errorf("insert dedup row: %w", err)
		}
	} else {
		// ── Fresh upload ──────────────────────────────────────────────────────
		const insertNew = `
			INSERT INTO files (user_id, object_key, sha256, size, content_type, ref_count, original_name)
			VALUES ($1, $2, $3, $4, $5, 1, $6)
			RETURNING id, user_id, object_key, sha256, size, content_type, ref_count, original_name, created_at, deleted_at`

		if err := tx.QueryRowx(insertNew, userID, objectKey, sha256Hash, size, contentType, originalName).
			StructScan(&newFile); err != nil {
			return nil, fmt.Errorf("insert new file: %w", err)
		}
	}

	// 4. Update storage_bytes_used atomically (gap fix 2).
	const updateUsage = `UPDATE users SET storage_bytes_used = storage_bytes_used + $1 WHERE id = $2`
	if _, err := tx.Exec(updateUsage, size, userID); err != nil {
		return nil, fmt.Errorf("update storage usage: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &SaveResult{File: newFile, Duplicate: existing != nil}, nil
}

// DeleteResult carries the outcome of a delete operation.
type DeleteResult struct {
	// ObjectKey is the storage key. Only set when the caller must delete the object.
	ObjectKey string
	// ShouldDeleteObject is true when ref_count reached 0 and the storage object must be removed.
	ShouldDeleteObject bool
}

// Delete removes a file record from the database.
//
// Deletion logic (gap fix 1):
//   - Decrement ref_count on the canonical row.
//   - If ref_count reaches 0 → set ShouldDeleteObject=true so the caller can
//     remove the backing object from storage.
//   - Decrement storage_bytes_used on the user.
func (m *MetadataStore) Delete(fileID, userID string) (*DeleteResult, error) {
	tx, err := m.db.Beginx()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// 1. Fetch the file (ensures ownership).
	const fetch = `
		SELECT id, user_id, object_key, sha256, size, content_type, ref_count, original_name, created_at, deleted_at
		FROM files WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL FOR UPDATE`

	var f FileMeta
	if err := tx.QueryRowx(fetch, fileID, userID).StructScan(&f); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("fetch file for delete: %w", err)
	}

	// 2. Find the canonical row for this sha256 to decrement its ref_count.
	const findCanon = `SELECT id, ref_count FROM files WHERE sha256 = $1 AND deleted_at IS NULL LIMIT 1`
	var canonID string
	var refCount int
	if err := tx.QueryRow(findCanon, f.SHA256).Scan(&canonID, &refCount); err != nil {
		return nil, fmt.Errorf("find canonical for delete: %w", err)
	}

	result := &DeleteResult{ObjectKey: f.ObjectKey}

	if refCount <= 1 {
		// Last reference — delete the file row and mark for storage deletion.
		const hardDelete = `DELETE FROM files WHERE id = $1`
		if _, err := tx.Exec(hardDelete, f.ID); err != nil {
			return nil, fmt.Errorf("hard delete file: %w", err)
		}
		result.ShouldDeleteObject = true
	} else {
		// Other users still reference this object — soft-delete only.
		const softDelete = `UPDATE files SET deleted_at = $1 WHERE id = $2`
		if _, err := tx.Exec(softDelete, time.Now(), f.ID); err != nil {
			return nil, fmt.Errorf("soft delete file: %w", err)
		}

		// Decrement ref_count on the canonical row (gap fix 1).
		const decrRef = `UPDATE files SET ref_count = ref_count - 1 WHERE id = $1`
		if _, err := tx.Exec(decrRef, canonID); err != nil {
			return nil, fmt.Errorf("decrement ref_count: %w", err)
		}
	}

	// 3. Reclaim storage quota (gap fix 2).
	const updateUsage = `UPDATE users SET storage_bytes_used = GREATEST(0, storage_bytes_used - $1) WHERE id = $2`
	if _, err := tx.Exec(updateUsage, f.Size, userID); err != nil {
		return nil, fmt.Errorf("update storage usage on delete: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit delete: %w", err)
	}

	return result, nil
}
