package handlers_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"miniio_s3/handlers"
	"miniio_s3/middleware"
	"miniio_s3/service"
	"miniio_s3/storage"
	"miniio_s3/testutil"
)

// ─── router setup ─────────────────────────────────────────────────────────────

func newFilesRouter(mockStore *testutil.MockStorage, mockMeta *testutil.MockMeta) *gin.Engine {
	svc := service.New(mockStore, mockMeta)
	fh := handlers.NewFileHandler(svc)

	r := gin.New()
	authed := r.Group("/files")
	authed.Use(middleware.Auth(authTestSecret))
	{
		authed.POST("/upload", fh.Upload)
		authed.GET("", fh.List)
		authed.GET("/:id", fh.Get)
		authed.GET("/:id/url", fh.PresignedURL)
		authed.DELETE("/:id", fh.Delete)
	}
	return r
}

// bearerToken mints a valid JWT for tests.
func bearerToken(userID string) string {
	claims := middleware.Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := t.SignedString([]byte(authTestSecret))
	return "Bearer " + signed
}

// buildMultipartBody writes a minimal multipart/form-data body with one file field.
func buildMultipartBody(filename, content string) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filename)
	fmt.Fprint(fw, content)
	mw.Close()
	return &buf, mw.FormDataContentType()
}

// ─── upload tests ─────────────────────────────────────────────────────────────

func TestUploadHandler_Success(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	r := newFilesRouter(mockStore, mockMeta)

	body, ct := buildMultipartBody("test.txt", "hello world")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/files/upload", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["file"] == nil {
		t.Error("expected 'file' field in response")
	}
	if resp["duplicate"] != false {
		t.Errorf("expected duplicate=false, got %v", resp["duplicate"])
	}
}

func TestUploadHandler_Duplicate_Returns200(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	r := newFilesRouter(mockStore, mockMeta)

	body1, ct1 := buildMultipartBody("a.txt", "same content")
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodPost, "/files/upload", body1)
	req1.Header.Set("Content-Type", ct1)
	req1.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first upload failed: %d", w1.Code)
	}

	body2, ct2 := buildMultipartBody("b.txt", "same content")
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/files/upload", body2)
	req2.Header.Set("Content-Type", ct2)
	req2.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Errorf("duplicate upload: expected 200, got %d", w2.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w2.Body).Decode(&resp)
	if resp["duplicate"] != true {
		t.Errorf("expected duplicate=true, got %v", resp["duplicate"])
	}
}

func TestUploadHandler_NoAuth_Returns401(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	r := newFilesRouter(mockStore, mockMeta)

	body, ct := buildMultipartBody("test.txt", "data")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/files/upload", body)
	req.Header.Set("Content-Type", ct)
	// No Authorization header
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestUploadHandler_QuotaExceeded_Returns429(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	mockMeta.SaveErr = storage.ErrQuotaExceeded
	r := newFilesRouter(mockStore, mockMeta)

	body, ct := buildMultipartBody("big.zip", "large")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/files/upload", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
}

func TestUploadHandler_MissingFileField_Returns400(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	r := newFilesRouter(mockStore, mockMeta)

	// Send a JSON body instead of multipart — no 'file' field
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/files/upload", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// ─── list tests ──────────────────────────────────────────────────────────────

func TestListHandler_ReturnsOwnedFiles(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	mockMeta.AddFile(&storage.FileMeta{ID: "f1", UserID: "user-1", SHA256: "aaa"})
	mockMeta.AddFile(&storage.FileMeta{ID: "f2", UserID: "user-1", SHA256: "bbb"})
	mockMeta.AddFile(&storage.FileMeta{ID: "f3", UserID: "user-2", SHA256: "ccc"})

	r := newFilesRouter(mockStore, mockMeta)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/files", nil)
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count := int(resp["count"].(float64))
	if count != 2 {
		t.Errorf("expected 2 files for user-1, got %d", count)
	}
}

func TestListHandler_NoAuth_Returns401(t *testing.T) {
	r := newFilesRouter(testutil.NewMockStorage(), testutil.NewMockMeta())
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/files", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ─── get tests ───────────────────────────────────────────────────────────────

func TestGetHandler_OwnedFile_Returns200(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockMeta := testutil.NewMockMeta()
	mockMeta.AddFile(&storage.FileMeta{ID: "file-abc", UserID: "user-1", SHA256: "xyz"})

	r := newFilesRouter(mockStore, mockMeta)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/files/file-abc", nil)
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}
}

func TestGetHandler_NotFound_Returns404(t *testing.T) {
	r := newFilesRouter(testutil.NewMockStorage(), testutil.NewMockMeta())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/files/does-not-exist", nil)
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestGetHandler_WrongUser_Returns404(t *testing.T) {
	mockMeta := testutil.NewMockMeta()
	mockMeta.AddFile(&storage.FileMeta{ID: "file-abc", UserID: "user-1", SHA256: "xyz"})
	r := newFilesRouter(testutil.NewMockStorage(), mockMeta)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/files/file-abc", nil)
	req.Header.Set("Authorization", bearerToken("user-2")) // wrong user
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for wrong-user access, got %d", w.Code)
	}
}

// ─── presigned URL tests ──────────────────────────────────────────────────────

func TestPresignedURLHandler_Returns200WithURL(t *testing.T) {
	mockMeta := testutil.NewMockMeta()
	mockMeta.AddFile(&storage.FileMeta{
		ID:        "file-abc",
		UserID:    "user-1",
		SHA256:    "xyz",
		ObjectKey: "users/user-1/xyz/doc.pdf",
	})
	r := newFilesRouter(testutil.NewMockStorage(), mockMeta)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/files/file-abc/url", nil)
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["url"] == "" {
		t.Error("expected non-empty URL in response")
	}
}

// ─── delete tests ─────────────────────────────────────────────────────────────

func TestDeleteHandler_OwnedFile_Returns200(t *testing.T) {
	mockMeta := testutil.NewMockMeta()
	mockMeta.AddFile(&storage.FileMeta{
		ID:        "file-abc",
		UserID:    "user-1",
		SHA256:    "xyz",
		ObjectKey: "users/user-1/xyz/doc.pdf",
	})
	r := newFilesRouter(testutil.NewMockStorage(), mockMeta)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/files/file-abc", nil)
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}
}

func TestDeleteHandler_NotFound_Returns404(t *testing.T) {
	r := newFilesRouter(testutil.NewMockStorage(), testutil.NewMockMeta())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/files/ghost", nil)
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDeleteHandler_StorageFailure_Returns200(t *testing.T) {
	// Storage failure on delete is logged but does not fail the request
	// (metadata is already removed at this point).
	mockStore := testutil.NewMockStorage()
	mockStore.DeleteErr = errors.New("storage unreachable")

	mockMeta := testutil.NewMockMeta()
	mockMeta.AddFile(&storage.FileMeta{
		ID:        "file-abc",
		UserID:    "user-1",
		SHA256:    "xyz",
		ObjectKey: "users/user-1/xyz/doc.pdf",
	})
	r := newFilesRouter(mockStore, mockMeta)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodDelete, "/files/file-abc", nil)
	req.Header.Set("Authorization", bearerToken("user-1"))
	r.ServeHTTP(w, req)

	// The handler should still return 200 — storage deletion is best-effort.
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 even with storage failure, got %d", w.Code)
	}
}
