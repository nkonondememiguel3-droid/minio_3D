package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
)

// APIKey is a machine-readable credential tied to a user.
type APIKey struct {
	ID         string     `db:"id"           json:"id"`
	UserID     string     `db:"user_id"      json:"user_id"`
	Name       string     `db:"name"         json:"name"`
	KeyHash    string     `db:"key_hash"     json:"-"`     // never returned to client
	Prefix     string     `db:"prefix"       json:"prefix"` // first 8 chars for display
	LastUsedAt *time.Time `db:"last_used_at" json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `db:"created_at"   json:"created_at"`
	ExpiresAt  *time.Time `db:"expires_at"   json:"expires_at,omitempty"`
}

// APIKeyStore handles API key persistence.
type APIKeyStore struct {
	db *sqlx.DB
}

// NewAPIKeyStore wraps an open sqlx.DB.
func NewAPIKeyStore(db *sqlx.DB) *APIKeyStore {
	return &APIKeyStore{db: db}
}

// CreateResult is returned once on key creation — the only time the raw key is available.
type CreateResult struct {
	APIKey
	RawKey string `json:"key"` // shown once, never stored
}

// Create generates a new API key, stores its hash, and returns the raw key.
// The raw key follows the format: msk_<32 random hex bytes>
func (s *APIKeyStore) Create(userID, name string, expiresAt *time.Time) (*CreateResult, error) {
	raw, err := generateRawKey()
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}

	hash := hashKey(raw)
	prefix := raw[:8]

	const q = `
		INSERT INTO api_keys (user_id, name, key_hash, prefix, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, name, key_hash, prefix, last_used_at, created_at, expires_at`

	var k APIKey
	if err := s.db.QueryRowx(q, userID, name, hash, prefix, expiresAt).StructScan(&k); err != nil {
		return nil, fmt.Errorf("insert api key: %w", err)
	}

	return &CreateResult{APIKey: k, RawKey: raw}, nil
}

// GetByHash looks up an API key by its SHA-256 hash.
// Used during request authentication.
func (s *APIKeyStore) GetByHash(rawKey string) (*APIKey, error) {
	hash := hashKey(rawKey)

	const q = `
		SELECT id, user_id, name, key_hash, prefix, last_used_at, created_at, expires_at
		FROM api_keys
		WHERE key_hash = $1`

	var k APIKey
	if err := s.db.QueryRowx(q, hash).StructScan(&k); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get api key: %w", err)
	}

	// Check expiry.
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return nil, ErrNotFound
	}

	return &k, nil
}

// ListByUser returns all API keys for a user (without key_hash).
func (s *APIKeyStore) ListByUser(userID string) ([]APIKey, error) {
	const q = `
		SELECT id, user_id, name, key_hash, prefix, last_used_at, created_at, expires_at
		FROM api_keys
		WHERE user_id = $1
		ORDER BY created_at DESC`

	var keys []APIKey
	if err := s.db.Select(&keys, q, userID); err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}

// Delete removes an API key by ID, asserting ownership.
func (s *APIKeyStore) Delete(id, userID string) error {
	const q = `DELETE FROM api_keys WHERE id = $1 AND user_id = $2`
	res, err := s.db.Exec(q, id, userID)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchLastUsed updates the last_used_at timestamp asynchronously.
// Fire-and-forget — called from the auth middleware.
func (s *APIKeyStore) TouchLastUsed(id string) {
	go func() {
		s.db.Exec(`UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, id)
	}()
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// generateRawKey produces a cryptographically random key with a "msk_" prefix.
func generateRawKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "msk_" + hex.EncodeToString(b), nil
}

// hashKey returns the SHA-256 hex digest of a raw key.
func hashKey(rawKey string) string {
	h := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(h[:])
}
