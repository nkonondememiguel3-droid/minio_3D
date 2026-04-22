//go:build integration

// Integration tests for DocumentStore.
// Requires a real PostgreSQL instance with both migrations applied.
//
// Run with:
//
//	TEST_DB_DSN="host=localhost port=5432 user=storageuser password=storagepass dbname=storagedb_test sslmode=disable" \
//	  go test -tags=integration ./storage/...
package storage_test

import (
	"testing"

	"miniio_s3/storage"
)

func TestDocIntegration_CreateAndGet(t *testing.T) {
	database := connectTestDB(t)
	userStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	u, err := userStore.CreateUser("doctest@example.com", "hash", 10<<30)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	doc, err := docStore.CreateDocument(u.ID, "test.pdf", "", "sha256abc", 2048)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if doc.ID == "" {
		t.Error("expected non-empty document ID")
	}
	if doc.Status != storage.StatusProcessing {
		t.Errorf("expected status=processing, got %s", doc.Status)
	}

	fetched, err := docStore.GetDocument(doc.ID, u.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if fetched.SHA256 != "sha256abc" {
		t.Errorf("sha256 mismatch: %s", fetched.SHA256)
	}
}

func TestDocIntegration_UpdateOriginalPath(t *testing.T) {
	database := connectTestDB(t)
	userStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	u, _ := userStore.CreateUser("pathtest@example.com", "hash", 10<<30)
	doc, _ := docStore.CreateDocument(u.ID, "f.pdf", "", "pathhash", 512)

	newPath := "documents/" + doc.ID + "/original.pdf"
	if err := docStore.UpdateOriginalPath(doc.ID, newPath); err != nil {
		t.Fatalf("UpdateOriginalPath: %v", err)
	}

	fetched, _ := docStore.GetDocumentByID(doc.ID)
	if fetched.OriginalMinioPath != newPath {
		t.Errorf("path = %q, want %q", fetched.OriginalMinioPath, newPath)
	}
}

func TestDocIntegration_MarkReady(t *testing.T) {
	database := connectTestDB(t)
	userStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	u, _ := userStore.CreateUser("ready@example.com", "hash", 10<<30)
	doc, _ := docStore.CreateDocument(u.ID, "f.pdf", "", "readyhash", 1024)

	if err := docStore.MarkReady(doc.ID, 5); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}

	fetched, _ := docStore.GetDocumentByID(doc.ID)
	if fetched.Status != storage.StatusReady {
		t.Errorf("status = %s, want ready", fetched.Status)
	}
	if fetched.TotalPages == nil || *fetched.TotalPages != 5 {
		t.Errorf("total_pages = %v, want 5", fetched.TotalPages)
	}
}

func TestDocIntegration_MarkFailed(t *testing.T) {
	database := connectTestDB(t)
	userStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	u, _ := userStore.CreateUser("failed@example.com", "hash", 10<<30)
	doc, _ := docStore.CreateDocument(u.ID, "f.pdf", "", "failedhash", 256)

	if err := docStore.MarkFailed(doc.ID, "pdfcpu exploded"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}

	fetched, _ := docStore.GetDocumentByID(doc.ID)
	if fetched.Status != storage.StatusFailed {
		t.Errorf("status = %s, want failed", fetched.Status)
	}
	if fetched.ErrorMessage == nil || *fetched.ErrorMessage != "pdfcpu exploded" {
		t.Errorf("error_message = %v, want 'pdfcpu exploded'", fetched.ErrorMessage)
	}
}

func TestDocIntegration_SaveAndGetPage(t *testing.T) {
	database := connectTestDB(t)
	userStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	u, _ := userStore.CreateUser("pages@example.com", "hash", 10<<30)
	doc, _ := docStore.CreateDocument(u.ID, "f.pdf", "", "pagehash", 4096)

	page, err := docStore.SavePage(doc.ID, 1, "documents/"+doc.ID+"/pages/1.pdf", 512)
	if err != nil {
		t.Fatalf("SavePage: %v", err)
	}
	if page.PageNumber != 1 {
		t.Errorf("page_number = %d, want 1", page.PageNumber)
	}

	fetched, err := docStore.GetPage(doc.ID, 1)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if fetched.MinioPath != page.MinioPath {
		t.Errorf("minio_path mismatch: %q vs %q", fetched.MinioPath, page.MinioPath)
	}
}

func TestDocIntegration_GetPageRange(t *testing.T) {
	database := connectTestDB(t)
	userStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	u, _ := userStore.CreateUser("range@example.com", "hash", 10<<30)
	doc, _ := docStore.CreateDocument(u.ID, "f.pdf", "", "rangehash", 8192)

	for i := 1; i <= 5; i++ {
		docStore.SavePage(doc.ID, i, "documents/"+doc.ID+"/pages/"+string(rune('0'+i))+".pdf", 256)
	}

	pages, err := docStore.GetPageRange(doc.ID, 2, 4)
	if err != nil {
		t.Fatalf("GetPageRange: %v", err)
	}
	if len(pages) != 3 {
		t.Errorf("expected 3 pages (2-4), got %d", len(pages))
	}
	if pages[0].PageNumber != 2 || pages[2].PageNumber != 4 {
		t.Errorf("wrong page numbers: %d..%d", pages[0].PageNumber, pages[2].PageNumber)
	}
}

func TestDocIntegration_SavePage_Idempotent(t *testing.T) {
	database := connectTestDB(t)
	userStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	u, _ := userStore.CreateUser("idem@example.com", "hash", 10<<30)
	doc, _ := docStore.CreateDocument(u.ID, "f.pdf", "", "idemhash", 1024)

	// Save same page twice — ON CONFLICT DO UPDATE should prevent error.
	docStore.SavePage(doc.ID, 1, "path/v1.pdf", 100)
	p2, err := docStore.SavePage(doc.ID, 1, "path/v2.pdf", 200)
	if err != nil {
		t.Fatalf("second SavePage: %v", err)
	}
	if p2.MinioPath != "path/v2.pdf" {
		t.Errorf("expected updated path path/v2.pdf, got %s", p2.MinioPath)
	}
}

func TestDocIntegration_Webhook_CreateAndGet(t *testing.T) {
	database := connectTestDB(t)
	userStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	u, _ := userStore.CreateUser("webhook@example.com", "hash", 10<<30)
	doc, _ := docStore.CreateDocument(u.ID, "f.pdf", "", "webhookhash", 512)

	_, err := docStore.CreateWebhook(u.ID, doc.ID, "https://example.com/cb", "secret123")
	if err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	hooks, err := docStore.GetWebhooksForDocument(doc.ID)
	if err != nil {
		t.Fatalf("GetWebhooksForDocument: %v", err)
	}
	if len(hooks) != 1 {
		t.Errorf("expected 1 webhook, got %d", len(hooks))
	}
	if hooks[0].URL != "https://example.com/cb" {
		t.Errorf("url = %q, want https://example.com/cb", hooks[0].URL)
	}
}

func TestDocIntegration_GetDocument_WrongUser_NotFound(t *testing.T) {
	database := connectTestDB(t)
	userStore := storage.NewMetadataStore(database)
	docStore := storage.NewDocumentStore(database)

	u1, _ := userStore.CreateUser("owner@example.com", "hash", 10<<30)
	u2, _ := userStore.CreateUser("other@example.com", "hash", 10<<30)
	doc, _ := docStore.CreateDocument(u1.ID, "f.pdf", "", "ownerhash", 512)

	_, err := docStore.GetDocument(doc.ID, u2.ID)
	if err == nil {
		t.Error("expected ErrNotFound for wrong user, got nil")
	}
}
