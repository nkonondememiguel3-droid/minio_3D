package service

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"

	"miniio_s3/storage"
)

// MetadataRepository is the interface the service depends on.
// Both *storage.MetadataStore and testutil.MockMeta satisfy it.
type MetadataRepository interface {
	GetByHash(hash string) (*storage.FileMeta, error)
	GetByID(id, userID string) (*storage.FileMeta, error)
	ListByUser(userID string) ([]storage.FileMeta, error)
	Save(userID, objectKey, sha256Hash, contentType, originalName string, size int64) (*storage.SaveResult, error)
	Delete(fileID, userID string) (*storage.DeleteResult, error)
}

// FileService orchestrates uploads, deduplication, quota checks, and deletions.
type FileService struct {
	store    storage.Storage
	metadata MetadataRepository
}

// New creates a FileService.
func New(store storage.Storage, metadata MetadataRepository) *FileService {
	return &FileService{store: store, metadata: metadata}
}

// FileUploadResult is the outcome returned to the HTTP handler after a file upload.
type FileUploadResult struct {
	File      storage.FileMeta
	Duplicate bool
}

// Upload performs the full upload pipeline:
//  1. Stream SHA-256 computation (no full-file buffer)
//  2. Quota pre-check + dedup check inside DB transaction
//  3. Upload to object storage only if content is new
//  4. Persist metadata (with ref_count and quota updates)
func (s *FileService) Upload(ctx context.Context, userID string, header *multipart.FileHeader) (*FileUploadResult, error) {
	src, err := header.Open()
	if err != nil {
		return nil, fmt.Errorf("open upload: %w", err)
	}
	defer src.Close()

	hash, err := storage.ComputeSHA256(src)
	if err != nil {
		return nil, fmt.Errorf("compute hash: %w", err)
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek after hash: %w", err)
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = detectContentType(header.Filename)
	}

	objectKey := buildObjectKey(userID, hash, header.Filename)
	originalName := filepath.Base(header.Filename)

	saveResult, err := s.metadata.Save(
		userID, objectKey, hash,
		contentType, originalName,
		header.Size,
	)
	if err != nil {
		return nil, err
	}

	if !saveResult.Duplicate {
		if _, err := s.store.Upload(ctx, objectKey, src, header.Size, contentType); err != nil {
			_ = s.deleteOrphanedMeta(saveResult.File.ID, userID)
			return nil, fmt.Errorf("storage upload: %w", err)
		}
	}

	return &FileUploadResult{File: saveResult.File, Duplicate: saveResult.Duplicate}, nil
}

func (s *FileService) GetMeta(fileID, userID string) (*storage.FileMeta, error) {
	return s.metadata.GetByID(fileID, userID)
}

func (s *FileService) ListFiles(userID string) ([]storage.FileMeta, error) {
	return s.metadata.ListByUser(userID)
}

func (s *FileService) PresignedURL(ctx context.Context, fileID, userID string) (string, error) {
	f, err := s.metadata.GetByID(fileID, userID)
	if err != nil {
		return "", err
	}
	return s.store.GetPresignedURL(ctx, f.ObjectKey)
}

func (s *FileService) Delete(ctx context.Context, fileID, userID string) error {
	result, err := s.metadata.Delete(fileID, userID)
	if err != nil {
		return err
	}
	if result.ShouldDeleteObject {
		if err := s.store.Delete(ctx, result.ObjectKey); err != nil {
			fmt.Printf("[WARN] storage delete failed for %s: %v\n", result.ObjectKey, err)
		}
	}
	return nil
}

func buildObjectKey(userID, hash, filename string) string {
	base := filepath.Base(filename)
	safe := strings.Map(func(r rune) rune {
		if r == ' ' {
			return '_'
		}
		return r
	}, base)
	return fmt.Sprintf("users/%s/%s/%s", userID, hash[:16], safe)
}

func detectContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".txt":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".json":
		return "application/json"
	case ".zip":
		return "application/zip"
	case ".mp4":
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

func (s *FileService) deleteOrphanedMeta(fileID, userID string) error {
	_, err := s.metadata.Delete(fileID, userID)
	return err
}
