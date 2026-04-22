package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"miniio_s3/middleware"
	"miniio_s3/service"
	"miniio_s3/storage"
)

// DocumentHandler handles all PDF document endpoints.
type DocumentHandler struct {
	svc *service.DocumentService
}

// NewDocumentHandler constructs a DocumentHandler.
func NewDocumentHandler(svc *service.DocumentService) *DocumentHandler {
	return &DocumentHandler{svc: svc}
}

// Upload accepts a PDF upload, stores it, and enqueues async extraction.
//
// POST /documents/upload
// Content-Type: multipart/form-data
// Field: "file" (must be a PDF)
//
// Response 202: { "document_id": "...", "status": "processing", "filename": "..." }
func (h *DocumentHandler) Upload(c *gin.Context) {
	userID := middleware.GetUserID(c)

	header, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "field 'file' is required"})
		return
	}

	result, err := h.svc.Upload(c.Request.Context(), userID, header)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidPDF):
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "uploaded file is not a valid PDF"})
		case errors.Is(err, storage.ErrQuotaExceeded):
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "storage quota exceeded"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	c.JSON(http.StatusAccepted, result)
}

// GetDocument returns a document's status and metadata.
//
// GET /documents/:id
func (h *DocumentHandler) GetDocument(c *gin.Context) {
	userID := middleware.GetUserID(c)
	docID := c.Param("id")

	doc, err := h.svc.GetDocument(c.Request.Context(), docID, userID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, doc)
}

// ListDocuments returns all documents for the authenticated user.
//
// GET /documents
func (h *DocumentHandler) ListDocuments(c *gin.Context) {
	userID := middleware.GetUserID(c)

	docs, err := h.svc.ListDocuments(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"documents": docs, "count": len(docs)})
}

// GetPage returns a presigned URL for a single extracted page.
//
// GET /documents/:id/pages/:page
// Response 200: { "url": "https://...", "page": 1, "document_id": "..." }
func (h *DocumentHandler) GetPage(c *gin.Context) {
	userID := middleware.GetUserID(c)
	docID := c.Param("id")

	pageNum, err := strconv.Atoi(c.Param("page"))
	if err != nil || pageNum < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "page must be a positive integer"})
		return
	}

	url, err := h.svc.GetPage(c.Request.Context(), docID, userID, pageNum)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("page %d not found", pageNum)})
		case errors.Is(err, service.ErrDocumentNotReady):
			c.JSON(http.StatusConflict, gin.H{
				"error":  "document is still processing",
				"status": "processing",
			})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"url":         url,
		"page":        pageNum,
		"document_id": docID,
	})
}

// GetPageRange streams pages start..end (inclusive) as a ZIP file.
//
// GET /documents/:id/pages?start=1&end=5
func (h *DocumentHandler) GetPageRange(c *gin.Context) {
	userID := middleware.GetUserID(c)
	docID := c.Param("id")

	start, err := strconv.Atoi(c.DefaultQuery("start", "1"))
	if err != nil || start < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "start must be a positive integer"})
		return
	}

	end, err := strconv.Atoi(c.Query("end"))
	if err != nil || end < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "end must be a positive integer"})
		return
	}

	if start > end {
		c.JSON(http.StatusBadRequest, gin.H{"error": "start must be <= end"})
		return
	}

	// Stream the ZIP directly into the response body — no buffering.
	zipFilename := fmt.Sprintf("document_%s_pages_%d-%d.zip", docID[:8], start, end)
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, zipFilename))

	err = h.svc.GetPageRange(c.Request.Context(), docID, userID, start, end, c.Writer)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrNotFound):
			// Headers already sent — we can only abort at this point.
			c.Abort()
		case errors.Is(err, service.ErrDocumentNotReady):
			c.JSON(http.StatusConflict, gin.H{
				"error":  "document is still processing",
				"status": "processing",
			})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		}
	}
}

// RegisterWebhook subscribes a URL for completion notifications.
//
// POST /documents/:id/webhook
// Body: { "url": "https://...", "secret": "optional-hmac-secret" }
func (h *DocumentHandler) RegisterWebhook(c *gin.Context) {
	userID := middleware.GetUserID(c)
	docID := c.Param("id")

	var req struct {
		URL    string `json:"url"    binding:"required,url"`
		Secret string `json:"secret"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	hook, err := h.svc.RegisterWebhook(c.Request.Context(), userID, docID, req.URL, req.Secret)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "document not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, hook)
}
