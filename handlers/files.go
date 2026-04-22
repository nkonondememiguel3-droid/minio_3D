package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"miniio_s3/middleware"
	"miniio_s3/service"
	"miniio_s3/storage"
)

// FileHandler handles all file-related HTTP endpoints.
type FileHandler struct {
	svc *service.FileService
}

// NewFileHandler constructs a FileHandler.
func NewFileHandler(svc *service.FileService) *FileHandler {
	return &FileHandler{svc: svc}
}

// Upload handles multipart file uploads.
//
// POST /files/upload
// Content-Type: multipart/form-data
// Field: "file"
func (h *FileHandler) Upload(c *gin.Context) {
	userID := middleware.GetUserID(c)

	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "field 'file' is required"})
		return
	}

	result, err := h.svc.Upload(c.Request.Context(), userID, header)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrQuotaExceeded):
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "storage quota exceeded"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	status := http.StatusCreated
	resp := gin.H{
		"file":      result.File,
		"duplicate": result.Duplicate,
	}
	if result.Duplicate {
		status = http.StatusOK
		resp["message"] = "file already exists; returning existing record"
	}

	c.JSON(status, resp)
}

// List returns all active files owned by the authenticated user.
//
// GET /files
func (h *FileHandler) List(c *gin.Context) {
	userID := middleware.GetUserID(c)

	files, err := h.svc.ListFiles(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"files": files, "count": len(files)})
}

// Get returns metadata for a single file.
//
// GET /files/:id
func (h *FileHandler) Get(c *gin.Context) {
	userID := middleware.GetUserID(c)
	fileID := c.Param("id")

	file, err := h.svc.GetMeta(fileID, userID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, file)
}

// PresignedURL generates a time-limited direct download URL.
//
// GET /files/:id/url
func (h *FileHandler) PresignedURL(c *gin.Context) {
	userID := middleware.GetUserID(c)
	fileID := c.Param("id")

	url, err := h.svc.PresignedURL(c.Request.Context(), fileID, userID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"url": url})
}

// Delete removes a file and, if it was the last reference, its storage object.
//
// DELETE /files/:id
func (h *FileHandler) Delete(c *gin.Context) {
	userID := middleware.GetUserID(c)
	fileID := c.Param("id")

	err := h.svc.Delete(c.Request.Context(), fileID, userID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "file deleted"})
}
