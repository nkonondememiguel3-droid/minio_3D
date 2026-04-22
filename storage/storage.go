package storage

import (
	"context"
	"io"
)

// Storage is the S3-compatible abstraction layer.
// Swap the implementation (MinIO → AWS S3 → Cloudflare R2) via config alone.
type Storage interface {
	// Upload streams a file to object storage and returns its object key.
	Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) (string, error)

	// GetPresignedURL returns a time-limited direct download URL.
	// The URL bypasses API auth — enforce TTL at the storage layer.
	GetPresignedURL(ctx context.Context, key string) (string, error)

	// Exists checks whether an object key is present in storage.
	Exists(ctx context.Context, key string) (bool, error)

	// Delete permanently removes an object from storage.
	// Only call this when ref_count has reached zero.
	Delete(ctx context.Context, key string) error
}
