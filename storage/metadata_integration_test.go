//go:build integration

// Integration tests for MetadataStore.
// These tests require a real PostgreSQL instance.
//
// Run with:
//
//	TEST_DB_DSN="host=localhost port=5432 user=storageuser password=storagepass dbname=storagedb_test sslmode=disable" \
//	  go test -tags=integration ./storage/...
package storage_test

import (
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"miniio_s3/db"
	"miniio_s3/storage"
)

func connectTestDB(t *testing.T) *sqlx.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		t.Skip("TEST_DB_DSN not set — skipping integration tests")
	}
	database, err := db.Connect(dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(func() {
		database.Exec("DELETE FROM files")
		database.Exec("DELETE FROM users")
		database.Close()
	})
	return database
}

func TestIntegration_CreateAndGetUser(t *testing.T) {
	database := connectTestDB(t)
	store := storage.NewMetadataStore(database)

	u, err := store.CreateUser("alice@example.com", "hashed", 10<<30)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == "" {
		t.Error("expected non-empty user ID")
	}

	fetched, err := store.GetUserByEmail("alice@example.com")
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if fetched.Email != "alice@example.com" {
		t.Errorf("email mismatch: %s", fetched.Email)
	}
}

func TestIntegration_SaveAndGetFile(t *testing.T) {
	database := connectTestDB(t)
	store := storage.NewMetadataStore(database)

	u, _ := store.CreateUser("bob@example.com", "hash", 10<<30)

	result, err := store.Save(u.ID, "users/bob/abc/file.pdf", "abc123hash", "application/pdf", "file.pdf", 1024)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if result.Duplicate {
		t.Error("first save should not be a duplicate")
	}
	if result.File.ID == "" {
		t.Error("expected non-empty file ID")
	}

	fetched, err := store.GetByID(result.File.ID, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fetched.SHA256 != "abc123hash" {
		t.Errorf("sha256 mismatch: %s", fetched.SHA256)
	}
}

func TestIntegration_Deduplication_RefCountIncrement(t *testing.T) {
	database := connectTestDB(t)
	store := storage.NewMetadataStore(database)

	u1, _ := store.CreateUser("user1@example.com", "h", 10<<30)
	u2, _ := store.CreateUser("user2@example.com", "h", 10<<30)

	r1, err := store.Save(u1.ID, "users/u1/hash/file.txt", "samehash", "text/plain", "file.txt", 512)
	if err != nil || r1.Duplicate {
		t.Fatalf("first save failed or marked duplicate: %v, %v", err, r1)
	}

	r2, err := store.Save(u2.ID, "users/u2/hash/file.txt", "samehash", "text/plain", "file.txt", 512)
	if err != nil {
		t.Fatalf("second save (dedup) failed: %v", err)
	}
	if !r2.Duplicate {
		t.Error("second save with same hash should be marked duplicate")
	}

	// Canonical row should have ref_count = 2
	canonical, err := store.GetByHash("samehash")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if canonical.RefCount < 2 {
		t.Errorf("expected ref_count >= 2, got %d", canonical.RefCount)
	}
}

func TestIntegration_Delete_LastRef_ShouldDeleteObject(t *testing.T) {
	database := connectTestDB(t)
	store := storage.NewMetadataStore(database)

	u, _ := store.CreateUser("carol@example.com", "h", 10<<30)
	r, _ := store.Save(u.ID, "users/carol/h/f.pdf", "onlyhash", "application/pdf", "f.pdf", 256)

	dr, err := store.Delete(r.File.ID, u.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !dr.ShouldDeleteObject {
		t.Error("last reference delete should set ShouldDeleteObject=true")
	}
	if dr.ObjectKey == "" {
		t.Error("expected non-empty ObjectKey in delete result")
	}

	// File should no longer be retrievable
	_, err = store.GetByID(r.File.ID, u.ID)
	if err == nil {
		t.Error("expected ErrNotFound after delete, got nil")
	}
}

func TestIntegration_QuotaExceeded(t *testing.T) {
	database := connectTestDB(t)
	store := storage.NewMetadataStore(database)

	// Create user with 100 byte quota
	u, _ := store.CreateUser("tiny@example.com", "h", 100)

	_, err := store.Save(u.ID, "key", "bighash", "application/pdf", "big.pdf", 200) // 200 > 100
	if err == nil {
		t.Fatal("expected quota exceeded error, got nil")
	}
	if err != storage.ErrQuotaExceeded {
		t.Errorf("expected ErrQuotaExceeded, got %v", err)
	}
}

func TestIntegration_Delete_SharedObject_NotDeletedFromStorage(t *testing.T) {
	database := connectTestDB(t)
	store := storage.NewMetadataStore(database)

	u1, _ := store.CreateUser("d1@example.com", "h", 10<<30)
	u2, _ := store.CreateUser("d2@example.com", "h", 10<<30)

	r1, _ := store.Save(u1.ID, "shared/key", "sharedhash", "text/plain", "f.txt", 10)
	r2, _ := store.Save(u2.ID, "shared/key", "sharedhash", "text/plain", "f.txt", 10)
	_ = r2

	// Delete user1's reference — should NOT trigger storage deletion
	dr, err := store.Delete(r1.File.ID, u1.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if dr.ShouldDeleteObject {
		t.Error("deleting one of two references should NOT trigger storage deletion")
	}
}
