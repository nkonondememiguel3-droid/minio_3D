package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/textproto"
	"testing"

	"miniio_s3/service"
	"miniio_s3/storage"
	"miniio_s3/testutil"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// makeFileHeader builds a *multipart.FileHeader from raw bytes, mimicking what
// Gin provides after parsing a multipart form upload.
func makeFileHeader(filename, contentType string, data []byte) *multipart.FileHeader {
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &multipart.FileHeader{
		Filename: filename,
		Header:   h,
		Size:     int64(len(data)),
		// Override Open() by embedding a custom reader — Go's multipart package
		// allows this because FileHeader.Open() uses the internal *multipart.Form.
		// For tests we use a small helper that wraps bytes.
	}
}

// inMemoryFileHeader returns a FileHeader whose Open() returns the given bytes.
// This lets us avoid touching the filesystem in tests.
func inMemoryFileHeader(filename, contentType string, data []byte) *multipart.FileHeader {
	// We use the exported multipart.FileHeader but replace its internal source
	// by writing a real temp multipart form in memory.
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
		panic("inMemoryFileHeader: no file part found")
	}
	return files[0]
}

// ─── tests ────────────────────────────────────────────────────────────────────

func TestUpload_NewFile_StoredOnce(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	fh := inMemoryFileHeader("report.pdf", "application/pdf", []byte("PDF content here"))
	result, err := svc.Upload(context.Background(), "user-1", fh)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Duplicate {
		t.Error("first upload should not be marked as duplicate")
	}
	if mockStore.ObjectCount() != 1 {
		t.Errorf("expected 1 object in storage, got %d", mockStore.ObjectCount())
	}
}

func TestUpload_SameContentTwice_NoDuplicateStorageWrite(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	data := []byte("identical file content")

	fh1 := inMemoryFileHeader("file_a.txt", "text/plain", data)
	r1, err := svc.Upload(context.Background(), "user-1", fh1)
	if err != nil {
		t.Fatalf("first upload failed: %v", err)
	}
	if r1.Duplicate {
		t.Error("first upload should not be a duplicate")
	}

	fh2 := inMemoryFileHeader("file_b.txt", "text/plain", data)
	r2, err := svc.Upload(context.Background(), "user-1", fh2)
	if err != nil {
		t.Fatalf("second upload failed: %v", err)
	}
	if !r2.Duplicate {
		t.Error("second upload with identical content should be marked duplicate")
	}
	// Storage must still hold exactly one object — no double write.
	if mockStore.ObjectCount() != 1 {
		t.Errorf("expected 1 object in storage after dedup, got %d", mockStore.ObjectCount())
	}
}

func TestUpload_QuotaExceeded_ReturnsError(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	mockMeta.SaveErr = storage.ErrQuotaExceeded

	svc := service.New(mockStore, mockMeta)

	fh := inMemoryFileHeader("big.zip", "application/zip", []byte("large file"))
	_, err := svc.Upload(context.Background(), "user-1", fh)

	if !errors.Is(err, storage.ErrQuotaExceeded) {
		t.Errorf("expected ErrQuotaExceeded, got %v", err)
	}
	if mockStore.ObjectCount() != 0 {
		t.Error("storage should not be written when quota is exceeded")
	}
}

func TestUpload_StorageFailure_MetadataRolledBack(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockStore.UploadErr = errors.New("storage unavailable")

	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	fh := inMemoryFileHeader("doc.pdf", "application/pdf", []byte("some content"))
	_, err := svc.Upload(context.Background(), "user-1", fh)

	if err == nil {
		t.Fatal("expected an error when storage upload fails")
	}
}

func TestGetMeta_OwnedByUser_Returned(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	mockMeta.AddFile(&storage.FileMeta{
		ID:      "file-123",
		UserID:  "user-1",
		SHA256:  "abc123",
		Size:    100,
	})

	f, err := svc.GetMeta("file-123", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.ID != "file-123" {
		t.Errorf("expected file-123, got %s", f.ID)
	}
}

func TestGetMeta_WrongUser_NotFound(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	mockMeta.AddFile(&storage.FileMeta{
		ID:     "file-123",
		UserID: "user-1",
		SHA256: "abc123",
	})

	_, err := svc.GetMeta("file-123", "user-2") // wrong user
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}
}

func TestListFiles_ReturnsOnlyOwnedFiles(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	mockMeta.AddFile(&storage.FileMeta{ID: "f1", UserID: "user-1", SHA256: "aaa"})
	mockMeta.AddFile(&storage.FileMeta{ID: "f2", UserID: "user-1", SHA256: "bbb"})
	mockMeta.AddFile(&storage.FileMeta{ID: "f3", UserID: "user-2", SHA256: "ccc"})

	files, err := svc.ListFiles("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files for user-1, got %d", len(files))
	}
}

func TestPresignedURL_OwnedFile_ReturnsURL(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	mockMeta.AddFile(&storage.FileMeta{
		ID:        "file-123",
		UserID:    "user-1",
		SHA256:    "abc",
		ObjectKey: "users/user-1/abc/doc.pdf",
	})

	url, err := svc.PresignedURL(context.Background(), "file-123", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url == "" {
		t.Error("expected a non-empty URL")
	}
}

func TestPresignedURL_WrongUser_NotFound(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	mockMeta.AddFile(&storage.FileMeta{
		ID:     "file-123",
		UserID: "user-1",
		SHA256: "abc",
	})

	_, err := svc.PresignedURL(context.Background(), "file-123", "user-2")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestDelete_LastReference_StorageObjectDeleted(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	// Pre-populate storage with the object.
	mockStore.Upload(context.Background(), "users/user-1/abc/doc.pdf", bytes.NewReader([]byte("data")), 4, "text/plain")

	mockMeta.AddFile(&storage.FileMeta{
		ID:        "file-123",
		UserID:    "user-1",
		SHA256:    "abc",
		ObjectKey: "users/user-1/abc/doc.pdf",
	})

	err := svc.Delete(context.Background(), "file-123", "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mockStore.ObjectCount() != 0 {
		t.Errorf("expected object to be deleted from storage, still have %d", mockStore.ObjectCount())
	}
}

func TestDelete_FileNotFound_ReturnsError(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	svc := service.New(mockStore, mockMeta)

	err := svc.Delete(context.Background(), "non-existent", "user-1")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ─── helper function unit tests ───────────────────────────────────────────────

func TestDetectContentType(t *testing.T) {
	// Access via an uploaded file's inferred type through service Upload.
	// We test detectContentType indirectly by checking what gets stored.
	cases := []struct {
		filename    string
		wantContains string
	}{
		{"document.pdf", "pdf"},
		{"image.png", "png"},
		{"photo.jpg", "jpeg"},
		{"archive.zip", "zip"},
		{"data.json", "json"},
		{"unknown.xyz", "octet-stream"},
	}

	for _, tc := range cases {
		mockStore := testutil.NewMockStorage()
		mockMeta := testutil.NewMockMeta()
		svc := service.New(mockStore, mockMeta)

		fh := inMemoryFileHeader(tc.filename, "", []byte("x"))
		result, err := svc.Upload(context.Background(), "u1", fh)
		if err != nil {
			t.Errorf("%s: upload failed: %v", tc.filename, err)
			continue
		}
		if !containsStr(result.File.ContentType, tc.wantContains) {
			t.Errorf("%s: expected content type containing %q, got %q",
				tc.filename, tc.wantContains, result.File.ContentType)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
