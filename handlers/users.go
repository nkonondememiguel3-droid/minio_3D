package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"miniio_s3/middleware"
	"miniio_s3/storage"
)

// UserHandler handles user profile endpoints.
type UserHandler struct {
	meta     *storage.MetadataStore
	docStore *storage.DocumentStore
}

// NewUserHandler constructs a UserHandler.
func NewUserHandler(meta *storage.MetadataStore, docStore *storage.DocumentStore) *UserHandler {
	return &UserHandler{meta: meta, docStore: docStore}
}

// Me returns the authenticated user's profile, storage usage, and document counts.
//
// GET /users/me
func (h *UserHandler) Me(c *gin.Context) {
	userID := middleware.GetUserID(c)

	user, err := h.meta.GetUserByID(userID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Document counts.
	docs, err := h.docStore.ListDocuments(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var ready, processing, failed int
	for _, d := range docs {
		switch d.Status {
		case storage.StatusReady:
			ready++
		case storage.StatusProcessing:
			processing++
		case storage.StatusFailed:
			failed++
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"id":    user.ID,
		"email": user.Email,
		"storage": gin.H{
			"used_bytes":  user.StorageBytesUsed,
			"quota_bytes": user.StorageQuotaBytes,
			"used_pct":    usedPct(user.StorageBytesUsed, user.StorageQuotaBytes),
		},
		"documents": gin.H{
			"total":      len(docs),
			"ready":      ready,
			"processing": processing,
			"failed":     failed,
		},
		"created_at": user.CreatedAt,
	})
}

func usedPct(used, quota int64) float64 {
	if quota == 0 {
		return 0
	}
	return float64(used) / float64(quota) * 100
}
