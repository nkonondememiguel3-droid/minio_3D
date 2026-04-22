package storage

import "time"

// User is the registered owner of files.
type User struct {
	ID                UUID   `db:"id"`
	Email             string `db:"email"`
	PasswordHash      string `db:"password_hash"`
	StorageBytesUsed  int64  `db:"storage_bytes_used"`
	StorageQuotaBytes int64  `db:"storage_quota_bytes"`
	CreatedAt         time.Time `db:"created_at"`
}

// FileMeta is a single file record owned by a user.
type FileMeta struct {
	ID           UUID      `db:"id"           json:"id"`
	UserID       UUID      `db:"user_id"       json:"user_id"`
	ObjectKey    string    `db:"object_key"    json:"-"`           // never exposed to clients
	SHA256       string    `db:"sha256"        json:"sha256"`
	Size         int64     `db:"size"          json:"size"`
	ContentType  string    `db:"content_type"  json:"content_type"`
	RefCount     int       `db:"ref_count"     json:"-"`           // internal only
	OriginalName string    `db:"original_name" json:"original_name"`
	CreatedAt    time.Time `db:"created_at"    json:"created_at"`
	DeletedAt    *time.Time `db:"deleted_at"   json:"deleted_at,omitempty"`
}

// UUID is an alias so we can swap to google/uuid if needed without touching all files.
type UUID = string
