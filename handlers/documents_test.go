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

func newDocRouter(mockStore *testutil.MockStorage, mockDoc *testutil.MockDocStore, mockEnq *testutil.MockEnqueuer) *gin.Engine {
	svc := service.NewDocumentService(mockStore, mockDoc, mockEnq)
	dh := handlers.NewDocumentHandler(svc)

	r := gin.New()
	docs := r.Group("/documents")
	docs.Use(middleware.Auth(authTestSecret))
	{
		docs.POST("/upload", dh.Upload)
		docs.GET("", dh.ListDocuments)
		docs.GET("/:id", dh.GetDocument)
		docs.GET("/:id/pages/:page", dh.GetPage)
		docs.GET("/:id/pages", dh.GetPageRange)
		docs.POST("/:id/webhook", dh.RegisterWebhook)
	}
	return r
}

// docBearerToken mints a JWT for document handler tests.
func docBearerToken(userID string) string {
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

// buildPDFMultipart creates a multipart body with a minimal PDF file.
func buildPDFMultipart(filename string) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", filename)
	fmt.Fprint(fw, "%PDF-1.4 minimal test content")
	mw.Close()
	return &buf, mw.FormDataContentType()
}

// ─── Upload tests ─────────────────────────────────────────────────────────────

func TestDocUploadHandler_ValidPDF_Returns202(t *testing.T) {
	mockStore := testutil.NewMockStorage()
	mockDoc := testutil.NewMockDocStore()
	mockEnq := testutil.NewMockEnqueuer()
	r := newDocRouter(mockStore, mockDoc, mockEnq)

	body, ct := buildPDFMultipart("report.pdf")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/documents/upload", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d — %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["document_id"] == "" {
		t.Error("expected document_id in response")
	}
	if resp["status"] != "processing" {
		t.Errorf("status = %v, want processing", resp["status"])
	}
}

func TestDocUploadHandler_NotPDF_Returns422(t *testing.T) {
	r := newDocRouter(testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer())

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "image.jpg")
	fmt.Fprint(fw, "this is not a PDF")
	mw.Close()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/documents/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", w.Code)
	}
}

func TestDocUploadHandler_NoAuth_Returns401(t *testing.T) {
	r := newDocRouter(testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer())
	body, ct := buildPDFMultipart("doc.pdf")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/documents/upload", body)
	req.Header.Set("Content-Type", ct)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestDocUploadHandler_MissingFileField_Returns400(t *testing.T) {
	r := newDocRouter(testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer())
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/documents/upload", bytes.NewBufferString("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestDocUploadHandler_QuotaExceeded_Returns429(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	mockDoc.CreateErr = storage.ErrQuotaExceeded
	r := newDocRouter(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	body, ct := buildPDFMultipart("big.pdf")
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/documents/upload", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", w.Code)
	}
}

// ─── GetDocument tests ────────────────────────────────────────────────────────

func TestDocGetHandler_OwnedDoc_Returns200(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	r := newDocRouter(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	// Upload first.
	body, ct := buildPDFMultipart("f.pdf")
	wu := httptest.NewRecorder()
	requ, _ := http.NewRequest(http.MethodPost, "/documents/upload", body)
	requ.Header.Set("Content-Type", ct)
	requ.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(wu, requ)
	var uploadResp map[string]string
	json.NewDecoder(wu.Body).Decode(&uploadResp)
	docID := uploadResp["document_id"]

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/documents/"+docID, nil)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}
}

func TestDocGetHandler_NotFound_Returns404(t *testing.T) {
	r := newDocRouter(testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer())
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/documents/does-not-exist", nil)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDocGetHandler_WrongUser_Returns404(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	r := newDocRouter(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	body, ct := buildPDFMultipart("f.pdf")
	wu := httptest.NewRecorder()
	requ, _ := http.NewRequest(http.MethodPost, "/documents/upload", body)
	requ.Header.Set("Content-Type", ct)
	requ.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(wu, requ)
	var uploadResp map[string]string
	json.NewDecoder(wu.Body).Decode(&uploadResp)
	docID := uploadResp["document_id"]

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/documents/"+docID, nil)
	req.Header.Set("Authorization", docBearerToken("user-2")) // wrong user
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for wrong user, got %d", w.Code)
	}
}

// ─── ListDocuments tests ──────────────────────────────────────────────────────

func TestDocListHandler_Returns200WithCount(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	r := newDocRouter(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	// Upload 2 docs.
	for i := 0; i < 2; i++ {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "f.pdf")
		fmt.Fprintf(fw, "%%PDF-1.4 content%d", i)
		mw.Close()
		wu := httptest.NewRecorder()
		requ, _ := http.NewRequest(http.MethodPost, "/documents/upload", &buf)
		requ.Header.Set("Content-Type", mw.FormDataContentType())
		requ.Header.Set("Authorization", docBearerToken("user-1"))
		r.ServeHTTP(wu, requ)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/documents", nil)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	count := int(resp["count"].(float64))
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}
}

// ─── GetPage tests ────────────────────────────────────────────────────────────

func TestDocGetPageHandler_ReadyDoc_Returns200(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	r := newDocRouter(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	body, ct := buildPDFMultipart("f.pdf")
	wu := httptest.NewRecorder()
	requ, _ := http.NewRequest(http.MethodPost, "/documents/upload", body)
	requ.Header.Set("Content-Type", ct)
	requ.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(wu, requ)
	var uploadResp map[string]string
	json.NewDecoder(wu.Body).Decode(&uploadResp)
	docID := uploadResp["document_id"]

	mockDoc.SetReady(docID, 3)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/documents/"+docID+"/pages/1", nil)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["url"] == "" {
		t.Error("expected non-empty url in response")
	}
}

func TestDocGetPageHandler_StillProcessing_Returns409(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	r := newDocRouter(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	body, ct := buildPDFMultipart("f.pdf")
	wu := httptest.NewRecorder()
	requ, _ := http.NewRequest(http.MethodPost, "/documents/upload", body)
	requ.Header.Set("Content-Type", ct)
	requ.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(wu, requ)
	var uploadResp map[string]string
	json.NewDecoder(wu.Body).Decode(&uploadResp)
	docID := uploadResp["document_id"]
	// Do NOT mark as ready.

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/documents/"+docID+"/pages/1", nil)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 while processing, got %d", w.Code)
	}
}

func TestDocGetPageHandler_InvalidPageParam_Returns400(t *testing.T) {
	r := newDocRouter(testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer())
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/documents/doc-1/pages/abc", nil)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestDocGetPageHandler_ZeroPageParam_Returns400(t *testing.T) {
	r := newDocRouter(testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer())
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/documents/doc-1/pages/0", nil)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for page=0, got %d", w.Code)
	}
}

// ─── GetPageRange tests ───────────────────────────────────────────────────────

func TestDocGetPageRangeHandler_InvalidParams_Returns400(t *testing.T) {
	r := newDocRouter(testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer())

	cases := []struct {
		query string
		label string
	}{
		{"?start=abc&end=3", "non-numeric start"},
		{"?start=1&end=abc", "non-numeric end"},
		{"?start=5&end=2", "start > end"},
		{"?start=1&end=0", "end=0"},
	}

	for _, tc := range cases {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/documents/doc-1/pages"+tc.query, nil)
		req.Header.Set("Authorization", docBearerToken("user-1"))
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("[%s] expected 400, got %d", tc.label, w.Code)
		}
	}
}

func TestDocGetPageRangeHandler_StillProcessing_Returns409(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	r := newDocRouter(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	body, ct := buildPDFMultipart("f.pdf")
	wu := httptest.NewRecorder()
	requ, _ := http.NewRequest(http.MethodPost, "/documents/upload", body)
	requ.Header.Set("Content-Type", ct)
	requ.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(wu, requ)
	var uploadResp map[string]string
	json.NewDecoder(wu.Body).Decode(&uploadResp)
	docID := uploadResp["document_id"]

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/documents/"+docID+"/pages?start=1&end=3", nil)
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409, got %d", w.Code)
	}
}

// ─── RegisterWebhook tests ────────────────────────────────────────────────────

func TestDocRegisterWebhookHandler_ValidURL_Returns201(t *testing.T) {
	mockDoc := testutil.NewMockDocStore()
	r := newDocRouter(testutil.NewMockStorage(), mockDoc, testutil.NewMockEnqueuer())

	body, ct := buildPDFMultipart("f.pdf")
	wu := httptest.NewRecorder()
	requ, _ := http.NewRequest(http.MethodPost, "/documents/upload", body)
	requ.Header.Set("Content-Type", ct)
	requ.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(wu, requ)
	var uploadResp map[string]string
	json.NewDecoder(wu.Body).Decode(&uploadResp)
	docID := uploadResp["document_id"]

	hookBody := `{"url":"https://example.com/webhook","secret":"mysecret"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/documents/"+docID+"/webhook", bytes.NewBufferString(hookBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["url"] != "https://example.com/webhook" {
		t.Errorf("url = %v, want https://example.com/webhook", resp["url"])
	}
}

func TestDocRegisterWebhookHandler_InvalidURL_Returns400(t *testing.T) {
	r := newDocRouter(testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer())

	hookBody := `{"url":"not-a-url"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/documents/doc-1/webhook", bytes.NewBufferString(hookBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid URL, got %d", w.Code)
	}
}

func TestDocRegisterWebhookHandler_DocNotFound_Returns404(t *testing.T) {
	r := newDocRouter(testutil.NewMockStorage(), testutil.NewMockDocStore(), testutil.NewMockEnqueuer())

	hookBody := `{"url":"https://example.com/hook"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/documents/ghost-id/webhook", bytes.NewBufferString(hookBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", docBearerToken("user-1"))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent doc, got %d", w.Code)
	}
}

// Ensure errors package is used (avoids import cycle warning).
var _ = errors.New
