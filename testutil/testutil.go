// Package testutil provides shared helpers, stubs, and mocks for all test packages.
package testutil

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"miniio_s3/storage"
)

// Mock Storage

// MockStorage is an in-memory implementation of storage.Storage for tests.
type MockStorage struct {
	mu      sync.RWMutex
	objects map[string][]byte

	// Hooks let individual tests inject failures.
	UploadErr     error
	GetPresignErr error
	DeleteErr     error
}

// NewMockStorage returns a ready-to-use MockStorage.
func NewMockStorage() *MockStorage {
	return &MockStorage{objects: make(map[string][]byte)}
}

func (m *MockStorage) Upload(_ context.Context, key string, body io.Reader, _ int64, _ string) (string, error) {
	if m.UploadErr != nil {
		return "", m.UploadErr
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.objects[key] = data
	m.mu.Unlock()
	return key, nil
}

func (m *MockStorage) GetPresignedURL(_ context.Context, key string) (string, error) {
	if m.GetPresignErr != nil {
		return "", m.GetPresignErr
	}
	return "https://mock-storage.test/" + key + "?token=signed", nil
}

func (m *MockStorage) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.objects[key]
	return ok, nil
}

func (m *MockStorage) Delete(_ context.Context, key string) error {
	if m.DeleteErr != nil {
		return m.DeleteErr
	}
	m.mu.Lock()
	delete(m.objects, key)
	m.mu.Unlock()
	return nil
}

// ObjectCount returns the number of objects currently stored.
func (m *MockStorage) ObjectCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.objects)
}

// Has returns true if key exists in mock storage.
func (m *MockStorage) Has(key string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.objects[key]
	return ok
}

// Mock MetadataStore

// MockMeta is a minimal in-memory stand-in for storage.MetadataStore.
// It tracks files and users for handler/service tests that must not touch Postgres.
type MockMeta struct {
	mu     sync.RWMutex
	users  map[string]*storage.User     // keyed by id
	files  map[string]*storage.FileMeta // keyed by id
	byHash map[string]*storage.FileMeta // keyed by sha256

	// Hooks
	SaveErr   error
	DeleteErr error
}

// NewMockMeta returns a ready-to-use MockMeta.
func NewMockMeta() *MockMeta {
	return &MockMeta{
		users:  make(map[string]*storage.User),
		files:  make(map[string]*storage.FileMeta),
		byHash: make(map[string]*storage.FileMeta),
	}
}

// AddUser seeds a user into the mock.
func (m *MockMeta) AddUser(u *storage.User) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[u.ID] = u
}

// AddFile seeds a file into the mock.
func (m *MockMeta) AddFile(f *storage.FileMeta) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *f
	m.files[f.ID] = &cp
	m.byHash[f.SHA256] = &cp
}

func (m *MockMeta) GetByHash(hash string) (*storage.FileMeta, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.byHash[hash]
	if !ok {
		return nil, storage.ErrNotFound
	}
	cp := *f
	return &cp, nil
}

func (m *MockMeta) GetByID(id, userID string) (*storage.FileMeta, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	f, ok := m.files[id]
	if !ok || f.UserID != userID || f.DeletedAt != nil {
		return nil, storage.ErrNotFound
	}
	cp := *f
	return &cp, nil
}

func (m *MockMeta) ListByUser(userID string) ([]storage.FileMeta, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []storage.FileMeta
	for _, f := range m.files {
		if f.UserID == userID && f.DeletedAt == nil {
			result = append(result, *f)
		}
	}
	return result, nil
}

func (m *MockMeta) Save(userID, objectKey, sha256Hash, contentType, originalName string, size int64) (*storage.SaveResult, error) {
	if m.SaveErr != nil {
		return nil, m.SaveErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	isDup := false
	if _, ok := m.byHash[sha256Hash]; ok {
		isDup = true
	}

	f := &storage.FileMeta{
		ID:           "file-" + sha256Hash[:8],
		UserID:       userID,
		ObjectKey:    objectKey,
		SHA256:       sha256Hash,
		Size:         size,
		ContentType:  contentType,
		OriginalName: originalName,
		CreatedAt:    now,
	}
	m.files[f.ID] = f
	m.byHash[sha256Hash] = f
	return &storage.SaveResult{File: *f, Duplicate: isDup}, nil
}

func (m *MockMeta) Delete(fileID, userID string) (*storage.DeleteResult, error) {
	if m.DeleteErr != nil {
		return nil, m.DeleteErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[fileID]
	if !ok || f.UserID != userID {
		return nil, storage.ErrNotFound
	}
	key := f.ObjectKey
	delete(m.files, fileID)
	delete(m.byHash, f.SHA256)
	return &storage.DeleteResult{ObjectKey: key, ShouldDeleteObject: true}, nil
}

// CreateUser satisfies the handlers.UserRepository interface.
func (m *MockMeta) CreateUser(email, passwordHash string, quotaBytes int64) (*storage.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Email == email {
			return nil, fmt.Errorf("duplicate email")
		}
	}
	u := &storage.User{
		ID:                "user-" + email,
		Email:             email,
		PasswordHash:      passwordHash,
		StorageQuotaBytes: quotaBytes,
		CreatedAt:         time.Now(),
	}
	m.users[u.ID] = u
	return u, nil
}

// GetUserByEmail satisfies the handlers.UserRepository interface.
func (m *MockMeta) GetUserByEmail(email string) (*storage.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, u := range m.users {
		if u.Email == email {
			cp := *u
			return &cp, nil
		}
	}
	return nil, storage.ErrNotFound
}

// Mock DocumentStore

// MockDocStore is an in-memory DocumentRepository for service/handler tests.
type MockDocStore struct {
	mu       sync.RWMutex
	docs     map[string]*storage.Document
	pages    map[string]map[int]*storage.DocumentPage // docID → pageNum → page
	webhooks map[string][]storage.WebhookSubscription

	// Hooks for injecting failures
	CreateErr     error
	GetErr        error
	UpdatePathErr error
	MarkFailedErr error
	SavePageErr   error
}

// NewMockDocStore returns a ready-to-use MockDocStore.
func NewMockDocStore() *MockDocStore {
	return &MockDocStore{
		docs:     make(map[string]*storage.Document),
		pages:    make(map[string]map[int]*storage.DocumentPage),
		webhooks: make(map[string][]storage.WebhookSubscription),
	}
}

func (m *MockDocStore) CreateDocument(userID, filename, minioPath, sha256 string, sizeBytes int64) (*storage.Document, error) {
	if m.CreateErr != nil {
		return nil, m.CreateErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	doc := &storage.Document{
		ID:                "doc-" + sha256[:8],
		UserID:            userID,
		Filename:          filename,
		OriginalMinioPath: minioPath,
		SHA256:            sha256,
		Status:            storage.StatusProcessing,
		SizeBytes:         sizeBytes,
	}
	m.docs[doc.ID] = doc
	return doc, nil
}

func (m *MockDocStore) GetDocument(id, userID string) (*storage.Document, error) {
	if m.GetErr != nil {
		return nil, m.GetErr
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.docs[id]
	if !ok || d.UserID != userID {
		return nil, storage.ErrNotFound
	}
	cp := *d
	return &cp, nil
}

func (m *MockDocStore) GetDocumentByID(id string) (*storage.Document, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.docs[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	cp := *d
	return &cp, nil
}

func (m *MockDocStore) ListDocuments(userID string) ([]storage.Document, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []storage.Document
	for _, d := range m.docs {
		if d.UserID == userID {
			result = append(result, *d)
		}
	}
	return result, nil
}

func (m *MockDocStore) MarkReady(docID string, totalPages int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.docs[docID]; ok {
		d.Status = storage.StatusReady
		d.TotalPages = &totalPages
	}
	return nil
}

func (m *MockDocStore) MarkFailed(docID, errMsg string) error {
	if m.MarkFailedErr != nil {
		return m.MarkFailedErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.docs[docID]; ok {
		d.Status = storage.StatusFailed
		d.ErrorMessage = &errMsg
	}
	return nil
}

func (m *MockDocStore) UpdateOriginalPath(docID, path string) error {
	if m.UpdatePathErr != nil {
		return m.UpdatePathErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.docs[docID]; ok {
		d.OriginalMinioPath = path
	}
	return nil
}

func (m *MockDocStore) SavePage(docID string, pageNumber int, minioPath string, sizeBytes int64) (*storage.DocumentPage, error) {
	if m.SavePageErr != nil {
		return nil, m.SavePageErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pages[docID] == nil {
		m.pages[docID] = make(map[int]*storage.DocumentPage)
	}
	p := &storage.DocumentPage{
		ID:         fmt.Sprintf("page-%s-%d", docID, pageNumber),
		DocumentID: docID,
		PageNumber: pageNumber,
		MinioPath:  minioPath,
		SizeBytes:  sizeBytes,
	}
	m.pages[docID][pageNumber] = p
	return p, nil
}

func (m *MockDocStore) GetPage(docID string, pageNumber int) (*storage.DocumentPage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if pages, ok := m.pages[docID]; ok {
		if p, ok := pages[pageNumber]; ok {
			cp := *p
			return &cp, nil
		}
	}
	return nil, storage.ErrNotFound
}

func (m *MockDocStore) GetPageRange(docID string, start, end int) ([]storage.DocumentPage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []storage.DocumentPage
	if pages, ok := m.pages[docID]; ok {
		for n := start; n <= end; n++ {
			if p, ok := pages[n]; ok {
				result = append(result, *p)
			}
		}
	}
	return result, nil
}

func (m *MockDocStore) ListPages(docID string) ([]storage.DocumentPage, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var result []storage.DocumentPage
	if pages, ok := m.pages[docID]; ok {
		for _, p := range pages {
			result = append(result, *p)
		}
	}
	return result, nil
}

func (m *MockDocStore) CreateWebhook(userID, docID, url, secret string) (*storage.WebhookSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w := storage.WebhookSubscription{
		ID:         "hook-" + docID,
		UserID:     userID,
		DocumentID: docID,
		URL:        url,
		Secret:     secret,
	}
	m.webhooks[docID] = append(m.webhooks[docID], w)
	return &w, nil
}

func (m *MockDocStore) GetWebhooksForDocument(docID string) ([]storage.WebhookSubscription, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.webhooks[docID], nil
}

// SetReady is a test helper that marks a document as ready with N pages
// and pre-populates its page records.
func (m *MockDocStore) SetReady(docID string, pageCount int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.docs[docID]; ok {
		d.Status = storage.StatusReady
		d.TotalPages = &pageCount
	}
	if m.pages[docID] == nil {
		m.pages[docID] = make(map[int]*storage.DocumentPage)
	}
	for i := 1; i <= pageCount; i++ {
		path := fmt.Sprintf("documents/%s/pages/%d.pdf", docID, i)
		m.pages[docID][i] = &storage.DocumentPage{
			ID:         fmt.Sprintf("page-%s-%d", docID, i),
			DocumentID: docID,
			PageNumber: i,
			MinioPath:  path,
			SizeBytes:  1024,
		}
	}
}

// Mock TaskEnqueuer

// MockEnqueuer records enqueued tasks and can inject errors.
type MockEnqueuer struct {
	mu         sync.Mutex
	Tasks      []EnqueuedTask
	EnqueueErr error
}

// EnqueuedTask records one enqueue call.
type EnqueuedTask struct {
	DocID  string
	UserID string
}

// NewMockEnqueuer returns a ready-to-use MockEnqueuer.
func NewMockEnqueuer() *MockEnqueuer {
	return &MockEnqueuer{}
}

// EnqueueTask satisfies service.TaskEnqueuer.
func (m *MockEnqueuer) EnqueueTask(_ context.Context, docID, userID string) error {
	if m.EnqueueErr != nil {
		return m.EnqueueErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Tasks = append(m.Tasks, EnqueuedTask{DocID: docID, UserID: userID})
	return nil
}

// Count returns how many tasks were enqueued.
func (m *MockEnqueuer) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Tasks)
}
