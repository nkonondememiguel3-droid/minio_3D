package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"miniio_s3/middleware"
)

const testSecret = "test-secret-key"

func init() {
	gin.SetMode(gin.TestMode)
}

// makeToken is a test helper that mints a signed JWT.
func makeToken(userID string, secret string, expiry time.Duration) string {
	claims := middleware.Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := t.SignedString([]byte(secret))
	return signed
}

func newRouter() *gin.Engine {
	r := gin.New()
	r.Use(middleware.Auth(testSecret))
	r.GET("/protected", func(c *gin.Context) {
		uid := middleware.GetUserID(c)
		c.JSON(http.StatusOK, gin.H{"user_id": uid})
	})
	return r
}

func TestAuth_ValidToken(t *testing.T) {
	token := makeToken("user-abc", testSecret, time.Hour)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	newRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — body: %s", w.Code, w.Body.String())
	}
	if !containsString(w.Body.String(), "user-abc") {
		t.Errorf("expected user_id in response, got: %s", w.Body.String())
	}
}

func TestAuth_MissingHeader(t *testing.T) {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/protected", nil)

	newRouter().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuth_WrongSecret(t *testing.T) {
	token := makeToken("user-abc", "wrong-secret", time.Hour)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	newRouter().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuth_ExpiredToken(t *testing.T) {
	token := makeToken("user-abc", testSecret, -time.Second) // already expired

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	newRouter().ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuth_MalformedHeader(t *testing.T) {
	cases := []string{
		"notbearer token",
		"Bearer",
		"Bearer ",
		"token-without-scheme",
	}
	for _, hdr := range cases {
		w := httptest.NewRecorder()
		req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", hdr)
		newRouter().ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("header %q: expected 401, got %d", hdr, w.Code)
		}
	}
}

func TestAuth_CaseInsensitiveBearer(t *testing.T) {
	token := makeToken("user-abc", testSecret, time.Hour)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "BEARER "+token) // uppercase

	newRouter().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for uppercase BEARER, got %d", w.Code)
	}
}

func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstring(s, sub))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
