package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinIOStorage implements the Storage interface backed by MinIO (or any S3-compatible store).
type MinIOStorage struct {
	client              *minio.Client
	bucketName          string
	presignedURLMinutes int
}

// NewMinIOStorage creates a MinIOStorage, ensures the target bucket exists,
// and returns a ready-to-use instance.
func NewMinIOStorage(endpoint, accessKey, secretKey, bucket string, useSSL bool, presignedMinutes int) (*MinIOStorage, error) {
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("minio client: %w", err)
	}

	ctx := context.Background()

	exists, err := client.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("minio bucket check: %w", err)
	}

	if !exists {
		if err := client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("minio make bucket: %w", err)
		}
	}

	return &MinIOStorage{
		client:              client,
		bucketName:          bucket,
		presignedURLMinutes: presignedMinutes,
	}, nil
}

// Upload streams body to object storage under key.
// The caller is responsible for passing the correct size; -1 triggers chunked upload.
func (m *MinIOStorage) Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) (string, error) {
	_, err := m.client.PutObject(ctx, m.bucketName, key, body, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return "", fmt.Errorf("upload %s: %w", key, err)
	}
	return key, nil
}

// GetPresignedURL returns a time-limited URL for direct object download.
func (m *MinIOStorage) GetPresignedURL(ctx context.Context, key string) (string, error) {
	ttl := time.Duration(m.presignedURLMinutes) * time.Minute
	url, err := m.client.PresignedGetObject(ctx, m.bucketName, key, ttl, nil)
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return url.String(), nil
}

// Exists reports whether key is present in storage.
func (m *MinIOStorage) Exists(ctx context.Context, key string) (bool, error) {
	_, err := m.client.StatObject(ctx, m.bucketName, key, minio.StatObjectOptions{})
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", key, err)
	}
	return true, nil
}

// Delete removes the object at key from storage.
// Must only be called when ref_count == 0.
func (m *MinIOStorage) Delete(ctx context.Context, key string) error {
	err := m.client.RemoveObject(ctx, m.bucketName, key, minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// ComputeSHA256 hashes a reader in a single streaming pass.
// The caller must seek back to the start before reading again.
func ComputeSHA256(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("sha256: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// OpenFileWithMeta opens a file, stat it, and sniffs its MIME type.
func OpenFileWithMeta(path string) (io.ReadSeekCloser, int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, "", err
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, "", err
	}

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		f.Close()
		return nil, 0, "", err
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, 0, "", err
	}

	contentType := http.DetectContentType(buf[:n])
	return f, info.Size(), contentType, nil
}

// isNotFound returns true for MinIO "key not found" errors.
func isNotFound(err error) bool {
	resp := minio.ToErrorResponse(err)
	return resp.Code == "NoSuchKey" || resp.StatusCode == http.StatusNotFound
}
