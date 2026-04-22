package service_test

import (
	"mime/multipart"
	"net/textproto"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"miniio_s3/service"
	"miniio_s3/storage"
	"miniio_s3/testutil"
)

// pdfBytes returns minimal PDF magic bytes — enough to pass validatePDF.
func pdfBytes() []byte { return []byte("%PDF-1.4 minimal test content") }

// notPDFBytes returns bytes that fail the PDF magic check.
func notPDFBytes() []byte { return []byte("this is not a PDF file at all") }

// ─── Upload tests ─────────────────────────────────────────────────────────────

func TestDocUpload_ValidPDF_Returns202(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockDoc := testutil.NewMockDocStore()
	mockEnq := testutil.NewMockEnqueuer()
	svc := service.NewDocumentService(mockStore, mockDoc, mockEnq)

	fh := inMemoryDocFileHeader("report.pdf", "application/pdf", pdfBytes())
	result, err := svc.Upload(context.Background(), "user-1", fh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.DocumentID == "" {
		t.Error("expected non-empty DocumentID")
	}
	if result.Status != storage.StatusProcessing {
		t.Errorf("status = %q, want processing", result.Status)
	}
	if result.Filename != "report.pdf" {
		t.Errorf("filename = %q, want report.pdf", result.Filename)
	}
	if mockEnq.Count() != 1 {
		t.Errorf("expected 1 enqueued task, got %d", mockEnq.Count())
	}
	if mockStore.ObjectCount() != 1 {
		t.Errorf("expected 1 object in storage, got %d", mockStore.ObjectCount())
	}
}

func TestDocUpload_NotPDF_ReturnsErrInvalidPDF(t *testing.T) {
	svc := service.NewDocumentService(
		testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer(),
	)
	fh := inMemoryDocFileHeader("image.jpg", "image/jpeg", notPDFBytes())
	_, err := svc.Upload(context.Background(), "user-1", fh)
	if !errors.Is(err, service.ErrInvalidPDF) {
		t.Errorf("expected ErrInvalidPDF, got %v", err)
	}
}

func TestDocUpload_StorageFails_ReturnsError(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockStore.UploadErr = errors.New("minio down")
	svc := service.NewDocumentService(mockStore, testutil.NewMockDocStore(), testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("doc.pdf", "application/pdf", pdfBytes())
	_, err := svc.Upload(context.Background(), "user-1", fh)
	if err == nil {
		t.Fatal("expected error when storage fails")
	}
}

func TestDocUpload_EnqueueFails_ReturnsError(t *testing.T) {
	mockEnq := testutil.NewMockEnqueuer()
	mockEnq.EnqueueErr = errors.New("redis unavailable")
	svc := service.NewDocumentService(
		testutil.NewMockStorage(), testutil.NewMockDocStore(), mockEnq,
	)
	fh := inMemoryDocFileHeader("doc.pdf", "application/pdf", pdfBytes())
	_, err := svc.Upload(context.Background(), "user-1", fh)
	if err == nil {
		t.Fatal("expected error when enqueue fails")
	}
}

// ─── GetDocument tests ────────────────────────────────────────────────────────

func TestGetDocument_OwnedDoc_Returned(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)

	doc, err := svc.GetDocument(context.Background(), result.DocumentID, "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.ID != result.DocumentID {
		t.Errorf("ID mismatch: %q vs %q", doc.ID, result.DocumentID)
	}
}

func TestGetDocument_WrongUser_NotFound(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)

	_, err := svc.GetDocument(context.Background(), result.DocumentID, "user-2")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}
}

// ─── GetPage tests ────────────────────────────────────────────────────────────

func TestGetPage_ReadyDocument_ReturnsURL(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)
	mockDoc.SetReady(result.DocumentID, 3)

	url, err := svc.GetPage(context.Background(), result.DocumentID, "user-1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url == "" {
		t.Error("expected non-empty URL")
	}
}

func TestGetPage_ProcessingDocument_NotReady(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)
	// Not calling SetReady — document remains "processing".

	_, err := svc.GetPage(context.Background(), result.DocumentID, "user-1", 1)
	if !errors.Is(err, service.ErrDocumentNotReady) {
		t.Errorf("expected ErrDocumentNotReady, got %v", err)
	}
}

func TestGetPage_PageOutOfRange_NotFound(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)
	mockDoc.SetReady(result.DocumentID, 2)

	_, err := svc.GetPage(context.Background(), result.DocumentID, "user-1", 99)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound for out-of-range page, got %v", err)
	}
}

// ─── GetPageRange tests ───────────────────────────────────────────────────────

func TestGetPageRange_ValidRange_ZIPProduced(t *testing.T) {
	// Run a real HTTP server so the ZIP streaming code can fetch page bytes.
	pageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(pdfBytes())
	}))
	defer pageSrv.Close()

	mockDoc := testutil.NewMockDocStore()
	mockStore := &realURLMockStorage{testURL: pageSrv.URL}
	svc := service.NewDocumentService(mockStore, mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)
	mockDoc.SetReady(result.DocumentID, 3)

	var buf bytes.Buffer
	err := svc.GetPageRange(context.Background(), result.DocumentID, "user-1", 1, 3, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected non-empty ZIP output")
	}
}

func TestGetPageRange_DocumentNotReady_Error(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)

	var buf bytes.Buffer
	err := svc.GetPageRange(context.Background(), result.DocumentID, "user-1", 1, 3, &buf)
	if !errors.Is(err, service.ErrDocumentNotReady) {
		t.Errorf("expected ErrDocumentNotReady, got %v", err)
	}
}

func TestGetPageRange_StartGreaterThanEnd_Error(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)
	mockDoc.SetReady(result.DocumentID, 5)

	var buf bytes.Buffer
	err := svc.GetPageRange(context.Background(), result.DocumentID, "user-1", 5, 2, &buf)
	if err == nil {
		t.Error("expected error for start > end")
	}
}

// ─── RegisterWebhook tests ────────────────────────────────────────────────────

func TestRegisterWebhook_OwnedDoc_Registered(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)

	hook, err := svc.RegisterWebhook(context.Background(), "user-1", result.DocumentID, "https://example.com/hook", "secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hook.URL != "https://example.com/hook" {
		t.Errorf("URL = %q, want https://example.com/hook", hook.URL)
	}
}

func TestRegisterWebhook_WrongUser_NotFound(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", pdfBytes())
	result, _ := svc.Upload(context.Background(), "user-1", fh)

	_, err := svc.RegisterWebhook(context.Background(), "user-2", result.DocumentID, "https://x.com", "")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}
}

// ─── ListDocuments tests ──────────────────────────────────────────────────────

func TestListDocuments_ReturnsOnlyOwned(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	svc := service.NewDocumentService(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	for i := 0; i < 2; i++ {
		fh := inMemoryDocFileHeader("f.pdf", "application/pdf", append(pdfBytes(), byte(i)))
		svc.Upload(context.Background(), "user-1", fh)
	}
	fh := inMemoryDocFileHeader("f.pdf", "application/pdf", append(pdfBytes(), byte(99)))
	svc.Upload(context.Background(), "user-2", fh)

	docs, err := svc.ListDocuments(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 2 {
		t.Errorf("expected 2 docs for user-1, got %d", len(docs))
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// realURLMockStorage returns presigned URLs pointing to the given test server.
type realURLMockStorage struct {
	testURL string
}

func (m *realURLMockStorage) Upload(_ context.Context, key string, _ io.Reader, _ int64, _ string) (string, error) {
	return key, nil
}
func (m *realURLMockStorage) GetPresignedURL(_ context.Context, key string) (string, error) {
	return fmt.Sprintf("%s/%s", m.testURL, key), nil
}
func (m *realURLMockStorage) Exists(_ context.Context, _ string) (bool, error) { return true, nil }
func (m *realURLMockStorage) Delete(_ context.Context, _ string) error          { return nil }

// inMemoryDocFileHeader builds a multipart.FileHeader from raw bytes for service tests.
// It is separate from the one in file_service_test.go to keep packages independent.
func inMemoryDocFileHeader(filename, contentType string, data []byte) *multipart.FileHeader {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	fw, _ := mw.CreatePart(h)
	fw.Write(data)
	mw.Close()
	mr := multipart.NewReader(&buf, mw.Boundary())
	form, _ := mr.ReadForm(int64(len(data)) * 2)
	files := form.File["file"]
	if len(files) == 0 {
		panic("inMemoryDocFileHeader: no file part found")
	}
	return files[0]
}
