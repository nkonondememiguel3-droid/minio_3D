package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"miniio_s3/handlers"
	"miniio_s3/storage"
	"miniio_s3/testutil"
)

func init() {
	gin.SetMode(gin.TestMode)
}

const authTestSecret = "test-jwt-secret"

func newAuthRouter(mock *testutil.MockMeta) *gin.Engine {
	r := gin.New()
	h := handlers.NewAuthHandler(mock, authTestSecret, 24, 10*1024*1024*1024)
	r.POST("/auth/register", h.Register)
	r.POST("/auth/login", h.Login)
	return r
}

func TestRegister_Success(t *testing.T) {
	mock := testutil.NewMockMeta()
	r := newAuthRouter(mock)

	body := `{"email":"alice@example.com","password":"supersecret"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["token"] == "" {
		t.Error("expected a token in the response")
	}
}

func TestRegister_InvalidEmail(t *testing.T) {
	mock := testutil.NewMockMeta()
	r := newAuthRouter(mock)

	body := `{"email":"not-an-email","password":"supersecret"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestRegister_ShortPassword(t *testing.T) {
	mock := testutil.NewMockMeta()
	r := newAuthRouter(mock)

	body := `{"email":"alice@example.com","password":"short"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for password < 8 chars, got %d", w.Code)
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	mock := testutil.NewMockMeta()
	r := newAuthRouter(mock)

	body := `{"email":"alice@example.com","password":"supersecret"}`

	// First registration — must succeed
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	req1.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first register failed with %d", w1.Code)
	}

	// Second registration with same email — must conflict
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodPost, "/auth/register", bytes.NewBufferString(body))
	req2.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusConflict {
		t.Errorf("expected 409 for duplicate email, got %d", w2.Code)
	}
}

func TestLogin_Success(t *testing.T) {
	mock := testutil.NewMockMeta()

	// Seed a user with a real bcrypt hash.
	hash, _ := bcrypt.GenerateFromPassword([]byte("correctpassword"), bcrypt.MinCost)
	mock.AddUser(&storage.User{
		ID:           "user-1",
		Email:        "bob@example.com",
		PasswordHash: string(hash),
	})

	r := newAuthRouter(mock)

	body := `{"email":"bob@example.com","password":"correctpassword"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["token"] == "" {
		t.Error("expected token in response")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	mock := testutil.NewMockMeta()
	hash, _ := bcrypt.GenerateFromPassword([]byte("correctpassword"), bcrypt.MinCost)
	mock.AddUser(&storage.User{
		ID:           "user-1",
		Email:        "bob@example.com",
		PasswordHash: string(hash),
	})

	r := newAuthRouter(mock)

	body := `{"email":"bob@example.com","password":"wrongpassword"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	mock := testutil.NewMockMeta()
	r := newAuthRouter(mock)

	body := `{"email":"ghost@example.com","password":"password123"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/auth/login", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestLogin_MissingBody(t *testing.T) {
	mock := testutil.NewMockMeta()
	r := newAuthRouter(mock)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/auth/login", nil)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}
