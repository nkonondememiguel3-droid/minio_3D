package storage

import "time"

// DocumentStatus represents the processing lifecycle of an uploaded PDF.
type DocumentStatus string

const (
	StatusProcessing DocumentStatus = "processing"
	StatusReady      DocumentStatus = "ready"
	StatusFailed     DocumentStatus = "failed"
)

// Document represents an uploaded PDF document.
type Document struct {
	ID                UUID           `db:"id"                  json:"id"`
	UserID            UUID           `db:"user_id"             json:"user_id"`
	Filename          string         `db:"filename"            json:"filename"`
	OriginalMinioPath string         `db:"original_minio_path" json:"-"`
	SHA256            string         `db:"sha256"              json:"sha256"`
	Status            DocumentStatus `db:"status"              json:"status"`
	TotalPages        *int           `db:"total_pages"         json:"total_pages"`
	ErrorMessage      *string        `db:"error_message"       json:"error_message,omitempty"`
	SizeBytes         int64          `db:"size_bytes"          json:"size_bytes"`
	CreatedAt         time.Time      `db:"created_at"          json:"created_at"`
	UpdatedAt         time.Time      `db:"updated_at"          json:"updated_at"`
}

// DocumentPage represents one extracted page stored as a standalone PDF.
type DocumentPage struct {
	ID         UUID  `db:"id"          json:"id"`
	DocumentID UUID  `db:"document_id" json:"document_id"`
	PageNumber int   `db:"page_number" json:"page_number"`
	MinioPath  string `db:"minio_path"  json:"-"`
	SizeBytes  int64 `db:"size_bytes"  json:"size_bytes"`
}

// WebhookSubscription is a callback URL registered for a document.
type WebhookSubscription struct {
	ID         UUID      `db:"id"          json:"id"`
	UserID     UUID      `db:"user_id"     json:"user_id"`
	DocumentID UUID      `db:"document_id" json:"document_id"`
	URL        string    `db:"url"         json:"url"`
	Secret     string    `db:"secret"      json:"-"`
	CreatedAt  time.Time `db:"created_at"  json:"created_at"`
}
