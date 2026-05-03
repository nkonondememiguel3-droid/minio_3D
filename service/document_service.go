package service

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"time"

	"github.com/hibiken/asynq"

	"miniio_s3/storage"
	"miniio_s3/worker"
)

// interfaces

// DocumentRepository is the storage interface the service depends on.
// Satisfied by *storage.DocumentStore and testutil.MockDocStore.
type DocumentRepository interface {
	CreateDocument(userID, filename, minioPath, sha256 string, sizeBytes int64) (*storage.Document, error)
	GetDocument(id, userID string) (*storage.Document, error)
	GetDocumentByID(id string) (*storage.Document, error)
	ListDocuments(userID string) ([]storage.Document, error)
	MarkReady(docID string, totalPages int) error
	MarkFailed(docID, errMsg string) error
	UpdateOriginalPath(docID, path string) error
	SavePage(docID string, pageNumber int, minioPath string, sizeBytes int64) (*storage.DocumentPage, error)
	GetPage(docID string, pageNumber int) (*storage.DocumentPage, error)
	GetPageRange(docID string, start, end int) ([]storage.DocumentPage, error)
	ListPages(docID string) ([]storage.DocumentPage, error)
	CreateWebhook(userID, docID, url, secret string) (*storage.WebhookSubscription, error)
	GetWebhooksForDocument(docID string) ([]storage.WebhookSubscription, error)
	DeleteDocument(id, userID string) (*storage.Document, error)
}

// TaskEnqueuer abstracts task enqueueing so tests can avoid a real Redis.
// Satisfied by *AsynqEnqueuer and testutil.MockEnqueuer.
type TaskEnqueuer interface {
	EnqueueTask(ctx context.Context, docID, userID string) error
}

// ─── production enqueuer ──────────────────────────────────────────────────────

// AsynqEnqueuer wraps the real *asynq.Client for production.
type AsynqEnqueuer struct {
	client *asynq.Client
}

// NewAsynqEnqueuer returns a production-ready TaskEnqueuer.
func NewAsynqEnqueuer(client *asynq.Client) *AsynqEnqueuer {
	return &AsynqEnqueuer{client: client}
}

// EnqueueTask builds and enqueues a pdf:extract_pages task.
func (e *AsynqEnqueuer) EnqueueTask(ctx context.Context, docID, userID string) error {
	task, err := worker.NewExtractPagesTask(docID, userID)
	if err != nil {
		return fmt.Errorf("build task: %w", err)
	}
	_, err = e.client.EnqueueContext(ctx, task, asynq.Queue("default"))
	return err
}

// ─── service ──────────────────────────────────────────────────────────────────

// DocumentService orchestrates PDF uploads, async extraction queuing,
// and page retrieval for external consumers.
type DocumentService struct {
	store    storage.Storage
	docStore DocumentRepository
	enqueuer TaskEnqueuer
}

// NewDocumentService constructs a DocumentService.
func NewDocumentService(store storage.Storage, docStore DocumentRepository, enqueuer TaskEnqueuer) *DocumentService {
	return &DocumentService{store: store, docStore: docStore, enqueuer: enqueuer}
}

// UploadResult is returned immediately after the PDF is stored.
type UploadResult struct {
	DocumentID string                 `json:"document_id"`
	Status     storage.DocumentStatus `json:"status"`
	Filename   string                 `json:"filename"`
}

// Upload stores the original PDF and enqueues the extraction task.
// Returns 202 immediately — extraction is async.
func (s *DocumentService) Upload(ctx context.Context, userID string, header *multipart.FileHeader) (*UploadResult, error) {
	src, err := header.Open()
	if err != nil {
		return nil, fmt.Errorf("open upload: %w", err)
	}
	defer src.Close()

	// Validate PDF magic bytes.
	if err := validatePDF(src); err != nil {
		return nil, err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek: %w", err)
	}

	// SHA-256 for dedup / integrity.
	hash, err := storage.ComputeSHA256(src)
	if err != nil {
		return nil, fmt.Errorf("sha256: %w", err)
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek after hash: %w", err)
	}

	originalName := filepath.Base(header.Filename)

	// Create DB record first to obtain an ID for the MinIO path.
	doc, err := s.docStore.CreateDocument(userID, originalName, "", hash, header.Size)
	if err != nil {
		return nil, fmt.Errorf("create document record: %w", err)
	}

	// Upload original to MinIO.
	minioPath := fmt.Sprintf("documents/%s/original.pdf", doc.ID)
	if _, err := s.store.Upload(ctx, minioPath, src, header.Size, "application/pdf"); err != nil {
		_ = s.docStore.MarkFailed(doc.ID, "upload failed: "+err.Error())
		return nil, fmt.Errorf("store original: %w", err)
	}

	// Patch the minio path now we have it.
	if err := s.docStore.UpdateOriginalPath(doc.ID, minioPath); err != nil {
		return nil, fmt.Errorf("update minio path: %w", err)
	}

	// Enqueue async extraction.
	if err := s.enqueuer.EnqueueTask(ctx, doc.ID, userID); err != nil {
		return nil, fmt.Errorf("enqueue extraction: %w", err)
	}

	return &UploadResult{
		DocumentID: doc.ID,
		Status:     storage.StatusProcessing,
		Filename:   originalName,
	}, nil
}

// GetDocument returns document metadata, asserting user ownership.
func (s *DocumentService) GetDocument(ctx context.Context, docID, userID string) (*storage.Document, error) {
	return s.docStore.GetDocument(docID, userID)
}

// ListDocuments returns all documents for a user, newest first.
func (s *DocumentService) ListDocuments(ctx context.Context, userID string) ([]storage.Document, error) {
	return s.docStore.ListDocuments(userID)
}

// GetPage returns a presigned URL for a single extracted page.
func (s *DocumentService) GetPage(ctx context.Context, docID, userID string, pageNumber int) (string, error) {
	doc, err := s.docStore.GetDocument(docID, userID)
	if err != nil {
		return "", err
	}
	if doc.Status != storage.StatusReady {
		return "", ErrDocumentNotReady
	}
	page, err := s.docStore.GetPage(docID, pageNumber)
	if err != nil {
		return "", err
	}
	return s.store.GetPresignedURL(ctx, page.MinioPath)
}

// GetPageRange streams pages start..end (inclusive) as a ZIP archive into w.
func (s *DocumentService) GetPageRange(ctx context.Context, docID, userID string, start, end int, w io.Writer) error {
	doc, err := s.docStore.GetDocument(docID, userID)
	if err != nil {
		return err
	}
	if doc.Status != storage.StatusReady {
		return ErrDocumentNotReady
	}
	if doc.TotalPages != nil && end > *doc.TotalPages {
		end = *doc.TotalPages
	}
	if start < 1 {
		start = 1
	}
	if start > end {
		return fmt.Errorf("start (%d) must be <= end (%d)", start, end)
	}

	pages, err := s.docStore.GetPageRange(docID, start, end)
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		return storage.ErrNotFound
	}

	zw := zip.NewWriter(w)
	defer zw.Close()

	httpClient := &http.Client{Timeout: 30 * time.Second}
	for _, page := range pages {
		url, err := s.store.GetPresignedURL(ctx, page.MinioPath)
		if err != nil {
			return fmt.Errorf("presign page %d: %w", page.PageNumber, err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("build request page %d: %w", page.PageNumber, err)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("fetch page %d: %w", page.PageNumber, err)
		}
		defer resp.Body.Close()

		fw, err := zw.Create(fmt.Sprintf("page_%d.pdf", page.PageNumber))
		if err != nil {
			return fmt.Errorf("zip entry page %d: %w", page.PageNumber, err)
		}
		if _, err := io.Copy(fw, resp.Body); err != nil {
			return fmt.Errorf("write zip page %d: %w", page.PageNumber, err)
		}
	}
	return nil
}

// RegisterWebhook subscribes a URL for document completion notifications.
func (s *DocumentService) RegisterWebhook(ctx context.Context, userID, docID, url, secret string) (*storage.WebhookSubscription, error) {
	if _, err := s.docStore.GetDocument(docID, userID); err != nil {
		return nil, err
	}
	return s.docStore.CreateWebhook(userID, docID, url, secret)
}

// ListPages returns all pages for a ready document with a fresh presigned URL for each.
// This is the primary endpoint for external services — one call to get everything.
func (s *DocumentService) ListPages(ctx context.Context, docID, userID string) ([]PageWithURL, error) {
	doc, err := s.docStore.GetDocument(docID, userID)
	if err != nil {
		return nil, err
	}
	if doc.Status != storage.StatusReady {
		return nil, ErrDocumentNotReady
	}

	pages, err := s.docStore.ListPages(docID)
	if err != nil {
		return nil, err
	}

	result := make([]PageWithURL, 0, len(pages))
	for _, p := range pages {
		url, err := s.store.GetPresignedURL(ctx, p.MinioPath)
		if err != nil {
			return nil, fmt.Errorf("presign page %d: %w", p.PageNumber, err)
		}
		result = append(result, PageWithURL{
			PageNumber: p.PageNumber,
			SizeBytes:  p.SizeBytes,
			URL:        url,
		})
	}
	return result, nil
}

// DeleteDocument removes a document and all extracted pages from both storage and DB.
func (s *DocumentService) DeleteDocument(ctx context.Context, docID, userID string) error {
	// Fetch all pages before deleting the DB rows (CASCADE will remove them).
	pages, err := s.docStore.ListPages(docID)
	if err != nil {
		return fmt.Errorf("list pages for delete: %w", err)
	}

	// Delete the document row — CASCADE removes document_pages rows too.
	doc, err := s.docStore.DeleteDocument(docID, userID)
	if err != nil {
		return err
	}

	// Delete MinIO objects best-effort — log failures but don't fail the request.
	go func() {
		bgCtx := context.Background()
		if err := s.store.Delete(bgCtx, doc.OriginalMinioPath); err != nil {
			fmt.Printf("[WARN] delete original %s: %v\n", doc.OriginalMinioPath, err)
		}
		// All extracted pages.
		for _, p := range pages {
			if err := s.store.Delete(bgCtx, p.MinioPath); err != nil {
				fmt.Printf("[WARN] delete page %s: %v\n", p.MinioPath, err)
			}
		}
	}()

	return nil
}

// sentinel errors

var ErrDocumentNotReady = fmt.Errorf("document is not ready yet")
var ErrInvalidPDF = fmt.Errorf("file is not a valid PDF")

// PageWithURL bundles a page record with its fresh presigned URL.
type PageWithURL struct {
	PageNumber int    `json:"page"`
	SizeBytes  int64  `json:"size_bytes"`
	URL        string `json:"url"`
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// validatePDF checks the %PDF magic bytes at the start of the file.
func validatePDF(r io.Reader) error {
	header := make([]byte, 8)
	n, err := r.Read(header)
	if err != nil && err != io.EOF {
		return fmt.Errorf("read header: %w", err)
	}
	if n < 4 {
		return ErrInvalidPDF
	}
	if string(header[:4]) != "%PDF" {
		return ErrInvalidPDF
	}
	return nil
}
