package worker

import (
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
)

// Task type identifiers — these strings are the queue keys.
const (
	TypeExtractPages = "pdf:extract_pages"
	TypeFireWebhook  = "pdf:fire_webhook"
)

// ─── extract_pages task ───────────────────────────────────────────────────────

// ExtractPagesPayload is the data enqueued when a PDF is uploaded.
type ExtractPagesPayload struct {
	DocumentID string `json:"document_id"`
	UserID     string `json:"user_id"`
}

// NewExtractPagesTask creates an Asynq task for the page extraction pipeline.
func NewExtractPagesTask(docID, userID string) (*asynq.Task, error) {
	payload, err := json.Marshal(ExtractPagesPayload{
		DocumentID: docID,
		UserID:     userID,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal extract_pages payload: %w", err)
	}
	return asynq.NewTask(TypeExtractPages, payload), nil
}

// ─── fire_webhook task ────────────────────────────────────────────────────────

// FireWebhookPayload is enqueued after page extraction completes.
type FireWebhookPayload struct {
	DocumentID string `json:"document_id"`
	WebhookURL string `json:"webhook_url"`
	Secret     string `json:"secret"`
	Status     string `json:"status"` // "ready" or "failed"
	TotalPages int    `json:"total_pages"`
}

// NewFireWebhookTask creates an Asynq task to deliver a webhook notification.
func NewFireWebhookTask(p FireWebhookPayload) (*asynq.Task, error) {
	payload, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("marshal fire_webhook payload: %w", err)
	}
	return asynq.NewTask(TypeFireWebhook, payload), nil
}
