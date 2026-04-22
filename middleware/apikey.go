package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"miniio_s3/storage"
)

const apiKeyHeader = "X-API-Key"

// APIKeyLookup is the minimal interface the middleware needs.
type APIKeyLookup interface {
	GetByHash(rawKey string) (*storage.APIKey, error)
	TouchLastUsed(id string)
}

// APIKeyAuth returns a Gin middleware that authenticates via X-API-Key header.
func APIKeyAuth(store APIKeyLookup) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := c.GetHeader(apiKeyHeader)
		if raw == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "X-API-Key header missing"})
			return
		}

		key, err := store.GetByHash(raw)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired API key"})
			return
		}

		store.TouchLastUsed(key.ID)
		c.Set(UserIDKey, key.UserID)
		c.Next()
	}
}

// FlexAuth tries JWT Bearer first, then X-API-Key.
// This lets the same routes be called by both human sessions and machine clients.
func FlexAuth(jwtSecret string, store APIKeyLookup) gin.HandlerFunc {
	jwtMiddleware := Auth(jwtSecret)
	apiKeyMiddleware := APIKeyAuth(store)

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		apiKeyHeader := c.GetHeader(apiKeyHeader)

		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			// JWT path
			jwtMiddleware(c)
			return
		}

		if apiKeyHeader != "" {
			// API key path
			apiKeyMiddleware(c)
			return
		}

		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error": "provide either Authorization: Bearer <jwt> or X-API-Key: <key>",
		})
	}
}
