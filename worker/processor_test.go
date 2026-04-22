package worker_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hibiken/asynq"

	"miniio_s3/worker"
)

// buildFireWebhookTask is a helper that builds a real asynq.Task for the
// FireWebhook handler without needing a live Redis connection.
func buildFireWebhookTask(p worker.FireWebhookPayload) *asynq.Task {
	payload, _ := json.Marshal(p)
	return asynq.NewTask(worker.TypeFireWebhook, payload)
}

func TestHandleFireWebhook_DeliverySuccess(t *testing.T) {
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	proc := worker.NewProcessor(nil, nil, nil)
	task := buildFireWebhookTask(worker.FireWebhookPayload{
		DocumentID: "doc-1",
		WebhookURL: srv.URL,
		Status:     "ready",
		TotalPages: 3,
	})

	err := proc.HandleFireWebhook(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(received) == 0 {
		t.Error("webhook server received no body")
	}

	var body map[string]interface{}
	if err := json.Unmarshal(received, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["document_id"] != "doc-1" {
		t.Errorf("document_id = %v, want doc-1", body["document_id"])
	}
	if body["status"] != "ready" {
		t.Errorf("status = %v, want ready", body["status"])
	}
}

func TestHandleFireWebhook_HMACSignature(t *testing.T) {
	const secret = "test-secret"
	var gotSig string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Signature-SHA256")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	proc := worker.NewProcessor(nil, nil, nil)
	task := buildFireWebhookTask(worker.FireWebhookPayload{
		DocumentID: "doc-2",
		WebhookURL: srv.URL,
		Secret:     secret,
		Status:     "ready",
		TotalPages: 1,
	})

	if err := proc.HandleFireWebhook(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotSig == "" {
		t.Fatal("expected X-Signature-SHA256 header, got empty string")
	}

	// Verify signature manually.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	wantSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if gotSig != wantSig {
		t.Errorf("signature mismatch\n got  %s\n want %s", gotSig, wantSig)
	}
}

func TestHandleFireWebhook_NoSecret_NoHeader(t *testing.T) {
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Signature-SHA256")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	proc := worker.NewProcessor(nil, nil, nil)
	task := buildFireWebhookTask(worker.FireWebhookPayload{
		DocumentID: "doc-3",
		WebhookURL: srv.URL,
		Secret:     "", // no secret
		Status:     "failed",
	})

	if err := proc.HandleFireWebhook(context.Background(), task); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSig != "" {
		t.Errorf("expected no signature header when secret is empty, got %q", gotSig)
	}
}

func TestHandleFireWebhook_Non2xxResponse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	proc := worker.NewProcessor(nil, nil, nil)
	task := buildFireWebhookTask(worker.FireWebhookPayload{
		DocumentID: "doc-4",
		WebhookURL: srv.URL,
		Status:     "ready",
	})

	err := proc.HandleFireWebhook(context.Background(), task)
	if err == nil {
		t.Error("expected error for non-2xx response, got nil")
	}
}

func TestHandleFireWebhook_InvalidPayload_ReturnsError(t *testing.T) {
	proc := worker.NewProcessor(nil, nil, nil)
	task := asynq.NewTask(worker.TypeFireWebhook, []byte("not-json"))

	err := proc.HandleFireWebhook(context.Background(), task)
	if err == nil {
		t.Error("expected error for invalid JSON payload, got nil")
	}
}

func TestHandleFireWebhook_UnreachableServer_ReturnsError(t *testing.T) {
	proc := worker.NewProcessor(nil, nil, nil)
	task := buildFireWebhookTask(worker.FireWebhookPayload{
		DocumentID: "doc-5",
		WebhookURL: "http://localhost:19999/no-such-server",
		Status:     "ready",
	})

	err := proc.HandleFireWebhook(context.Background(), task)
	if err == nil {
		t.Error("expected error for unreachable server, got nil")
	}
}
