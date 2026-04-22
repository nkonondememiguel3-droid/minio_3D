package worker_test

import (
	"encoding/json"
	"testing"

	"miniio_s3/worker"
)

func TestNewExtractPagesTask_PayloadRoundtrip(t *testing.T) {
	task, err := worker.NewExtractPagesTask("doc-123", "user-456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if task.Type() != worker.TypeExtractPages {
		t.Errorf("type = %q, want %q", task.Type(), worker.TypeExtractPages)
	}

	var payload worker.ExtractPagesPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if payload.DocumentID != "doc-123" {
		t.Errorf("DocumentID = %q, want %q", payload.DocumentID, "doc-123")
	}
	if payload.UserID != "user-456" {
		t.Errorf("UserID = %q, want %q", payload.UserID, "user-456")
	}
}

func TestNewFireWebhookTask_PayloadRoundtrip(t *testing.T) {
	p := worker.FireWebhookPayload{
		DocumentID: "doc-abc",
		WebhookURL: "https://example.com/hook",
		Secret:     "mysecret",
		Status:     "ready",
		TotalPages: 5,
	}
	task, err := worker.NewFireWebhookTask(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if task.Type() != worker.TypeFireWebhook {
		t.Errorf("type = %q, want %q", task.Type(), worker.TypeFireWebhook)
	}

	var got worker.FireWebhookPayload
	if err := json.Unmarshal(task.Payload(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.DocumentID != p.DocumentID {
		t.Errorf("DocumentID mismatch: %q vs %q", got.DocumentID, p.DocumentID)
	}
	if got.TotalPages != p.TotalPages {
		t.Errorf("TotalPages mismatch: %d vs %d", got.TotalPages, p.TotalPages)
	}
	if got.Secret != p.Secret {
		t.Errorf("Secret mismatch")
	}
}

func TestNewExtractPagesTask_EmptyIDs(t *testing.T) {
	// Empty IDs are technically valid — validation happens at the service layer.
	task, err := worker.NewExtractPagesTask("", "")
	if err != nil {
		t.Fatalf("unexpected error for empty IDs: %v", err)
	}
	if task == nil {
		t.Error("expected non-nil task")
	}
}
