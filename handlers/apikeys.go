package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"miniio_s3/middleware"
	"miniio_s3/storage"
)

// APIKeyHandler manages API key creation, listing, and deletion.
type APIKeyHandler struct {
	store *storage.APIKeyStore
}

// NewAPIKeyHandler constructs an APIKeyHandler.
func NewAPIKeyHandler(store *storage.APIKeyStore) *APIKeyHandler {
	return &APIKeyHandler{store: store}
}

// Create generates a new API key.
//
// POST /auth/api-keys
// Body: { "name": "production backend", "expires_at": "2026-01-01T00:00:00Z" }
// Response 201: { "id": "...", "key": "msk_...", "prefix": "msk_xxxx", ... }
//
// The "key" field is returned ONCE and never again — store it securely.
func (h *APIKeyHandler) Create(c *gin.Context) {
	userID := middleware.GetUserID(c)

	var req struct {
		Name      string     `json:"name"       binding:"required"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	result, err := h.store.Create(userID, req.Name, req.ExpiresAt)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create API key"})
		return
	}

	c.JSON(http.StatusCreated, result)
}

// List returns all API keys for the authenticated user.
// Key hashes are never included in the response.
//
// GET /auth/api-keys
func (h *APIKeyHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)

	keys, err := h.store.ListByUser(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"api_keys": keys, "count": len(keys)})
}

// Delete revokes an API key by ID.
//
// DELETE /auth/api-keys/:id
func (h *APIKeyHandler) Delete(c *gin.Context) {
	userID := middleware.GetUserID(c)
	keyID := c.Param("id")

	err := h.store.Delete(keyID, userID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "API key revoked"})
}
