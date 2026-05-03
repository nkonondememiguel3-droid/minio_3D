package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"

	"miniio_s3/storage"
)

const (
	// pageExtractionConcurrency is the max number of pages extracted in parallel
	// per task. Matches the spec (5 concurrent per worker).
	pageExtractionConcurrency = 5

	// webhookTimeout is the HTTP timeout for outbound webhook delivery.
	webhookTimeout = 10 * time.Second

	// webhookMaxRetries controls how many times Asynq retries a failed webhook.
	webhookMaxRetries = 3
)

// Processor handles all background tasks.
type Processor struct {
	store    storage.Storage
	docStore *storage.DocumentStore
	client   *asynq.Client // used to enqueue webhook tasks after extraction
}

// NewProcessor constructs a Processor.
func NewProcessor(store storage.Storage, docStore *storage.DocumentStore, client *asynq.Client) *Processor {
	return &Processor{store: store, docStore: docStore, client: client}
}

// ExtractPages handler

// HandleExtractPages is the Asynq handler for TypeExtractPages.
// It downloads the original PDF, splits it page by page, uploads each page,
// and updates the document status in PostgreSQL.
func (p *Processor) HandleExtractPages(ctx context.Context, t *asynq.Task) error {
	var payload ExtractPagesPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	docID := payload.DocumentID
	log.Printf("[worker] starting extraction for document %s", docID)

	// ── 1. Fetch document record ──────────────────────────────────────────────
	doc, err := p.docStore.GetDocumentByID(docID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// Document was deleted before the worker picked up the task.
			// Return nil so Asynq does NOT retry — there is nothing to process.
			log.Printf("[worker] document %s not found (deleted before processing) — skipping", docID)
			return nil
		}
		return fmt.Errorf("get document %s: %w", docID, err)
	}

	// ── 2. Download original PDF from MinIO into a temp file ──────────────────
	// We write to disk rather than memory to avoid loading large PDFs entirely
	// into RAM. pdfcpu also works best with file paths.
	tmpDir, err := os.MkdirTemp("", "pdfextract-"+docID+"-*")
	if err != nil {
		return p.fail(docID, fmt.Errorf("create temp dir: %w", err))
	}
	defer os.RemoveAll(tmpDir) // always clean up temp files

	origPath := filepath.Join(tmpDir, "original.pdf")
	if err := p.downloadToFile(ctx, doc.OriginalMinioPath, origPath); err != nil {
		return p.fail(docID, fmt.Errorf("download original: %w", err))
	}

	// ── 3. Count pages ────────────────────────────────────────────────────────
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed

	pageCount, err := pdfcpuapi.PageCountFile(origPath)
	if err != nil {
		return p.fail(docID, fmt.Errorf("count pages: %w", err))
	}
	if pageCount == 0 {
		return p.fail(docID, fmt.Errorf("PDF has 0 pages"))
	}
	log.Printf("[worker] document %s has %d pages", docID, pageCount)

	// ── 4. Extract pages in parallel (concurrency = pageExtractionConcurrency) ─
	pagesDir := filepath.Join(tmpDir, "pages")
	if err := os.MkdirAll(pagesDir, 0755); err != nil {
		return p.fail(docID, fmt.Errorf("create pages dir: %w", err))
	}

	sem := make(chan struct{}, pageExtractionConcurrency)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)

	for pageNum := 1; pageNum <= pageCount; pageNum++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := p.extractAndUploadPage(ctx, conf, origPath, pagesDir, docID, n); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				log.Printf("[worker] page %d of %s failed: %v", n, docID, err)
			}
		}(pageNum)
	}

	wg.Wait()

	if firstErr != nil {
		return p.fail(docID, firstErr)
	}

	// ── 5. Mark document as ready ─────────────────────────────────────────────
	if err := p.docStore.MarkReady(docID, pageCount); err != nil {
		return fmt.Errorf("mark ready: %w", err)
	}
	log.Printf("[worker] document %s extraction complete (%d pages)", docID, pageCount)

	// ── 6. Fire webhooks asynchronously ───────────────────────────────────────
	p.enqueueWebhooks(docID, "ready", pageCount)

	return nil
}

// extractAndUploadPage extracts page n from origPath, uploads it to MinIO,
// and saves its metadata to PostgreSQL.
func (p *Processor) extractAndUploadPage(
	ctx context.Context,
	conf *model.Configuration,
	origPath, pagesDir, docID string,
	pageNum int,
) error {
	pageFile := filepath.Join(pagesDir, fmt.Sprintf("page_%d.pdf", pageNum))

	// pdfcpu extracts a page range into a directory.
	// We pass a single-page range: [n, n].
	pageRange := []string{fmt.Sprintf("%d", pageNum)}
	if err := pdfcpuapi.ExtractPagesFile(origPath, pagesDir, pageRange, conf); err != nil {
		return fmt.Errorf("pdfcpu extract page %d: %w", pageNum, err)
	}

	// pdfcpu names the output file like "original_1.pdf", "original_2.pdf", etc.
	// Rename to our standard name for predictability.
	pdfcpuOutput := filepath.Join(pagesDir, fmt.Sprintf("original_%d.pdf", pageNum))
	if err := os.Rename(pdfcpuOutput, pageFile); err != nil {
		// pdfcpu naming varies — fall back to scanning the dir
		pageFile, err = findExtractedPage(pagesDir, pageNum)
		if err != nil {
			return fmt.Errorf("locate extracted page %d: %w", pageNum, err)
		}
	}

	// Upload the extracted page to MinIO.
	minioPath := fmt.Sprintf("documents/%s/pages/%d.pdf", docID, pageNum)
	f, err := os.Open(pageFile)
	if err != nil {
		return fmt.Errorf("open page file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat page file: %w", err)
	}

	if _, err := p.store.Upload(ctx, minioPath, f, info.Size(), "application/pdf"); err != nil {
		return fmt.Errorf("upload page %d: %w", pageNum, err)
	}

	// Persist page metadata.
	if _, err := p.docStore.SavePage(docID, pageNum, minioPath, info.Size()); err != nil {
		return fmt.Errorf("save page metadata %d: %w", pageNum, err)
	}

	return nil
}

// downloadToFile streams a MinIO object to a local file path.
func (p *Processor) downloadToFile(ctx context.Context, minioKey, destPath string) error {
	// We use GetPresignedURL + http.Get to avoid coupling the worker to the
	// minio-go client directly — the Storage interface handles that.
	url, err := p.store.GetPresignedURL(ctx, minioKey)
	if err != nil {
		return fmt.Errorf("presign download: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create dest file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write to disk: %w", err)
	}
	return nil
}

// fail marks the document as failed in the DB and returns the error
// so Asynq can decide whether to retry.
func (p *Processor) fail(docID string, err error) error {
	_ = p.docStore.MarkFailed(docID, err.Error())
	p.enqueueWebhooks(docID, "failed", 0)
	return err
}

// enqueueWebhooks fetches registered webhooks for a document and enqueues
// a delivery task for each one.
func (p *Processor) enqueueWebhooks(docID, status string, totalPages int) {
	hooks, err := p.docStore.GetWebhooksForDocument(docID)
	if err != nil {
		log.Printf("[worker] get webhooks for %s: %v", docID, err)
		return
	}
	for _, hook := range hooks {
		task, err := NewFireWebhookTask(FireWebhookPayload{
			DocumentID: docID,
			WebhookURL: hook.URL,
			Secret:     hook.Secret,
			Status:     status,
			TotalPages: totalPages,
		})
		if err != nil {
			log.Printf("[worker] build webhook task: %v", err)
			continue
		}
		opts := []asynq.Option{
			asynq.MaxRetry(webhookMaxRetries),
			asynq.Timeout(webhookTimeout),
		}
		if _, err := p.client.Enqueue(task, opts...); err != nil {
			log.Printf("[worker] enqueue webhook: %v", err)
		}
	}
}

// ─── FireWebhook handler ──────────────────────────────────────────────────────

// HandleFireWebhook delivers a single webhook notification via HTTP POST.
func (p *Processor) HandleFireWebhook(_ context.Context, t *asynq.Task) error {
	var payload FireWebhookPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("unmarshal webhook payload: %w", err)
	}

	body, err := json.Marshal(map[string]interface{}{
		"document_id": payload.DocumentID,
		"status":      payload.Status,
		"total_pages": payload.TotalPages,
		"timestamp":   time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("marshal webhook body: %w", err)
	}

	// Sign the payload with HMAC-SHA256 if a secret is set.
	sig := ""
	if payload.Secret != "" {
		mac := hmac.New(sha256.New, []byte(payload.Secret))
		mac.Write(body)
		sig = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}

	req, err := http.NewRequest(http.MethodPost, payload.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if sig != "" {
		req.Header.Set("X-Signature-SHA256", sig)
	}

	client := &http.Client{Timeout: webhookTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("deliver webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned non-2xx: %d", resp.StatusCode)
	}

	log.Printf("[worker] webhook delivered to %s (status %d)", payload.WebhookURL, resp.StatusCode)
	return nil
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// findExtractedPage searches pagesDir for a file whose name contains the page number.
// pdfcpu names extracted files differently depending on the input filename.
func findExtractedPage(pagesDir string, pageNum int) (string, error) {
	pattern := filepath.Join(pagesDir, fmt.Sprintf("*_%d.pdf", pageNum))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no file found matching *_%d.pdf in %s", pageNum, pagesDir)
	}
	return matches[0], nil
}
