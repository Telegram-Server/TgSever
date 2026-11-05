package memory

import (
	"context"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// AppUpdateDocumentStore 是 store.AppUpdateDocumentStore 的内存实现。
type AppUpdateDocumentStore struct {
	mu   sync.Mutex
	docs map[string]domain.AppUpdateDocument
}

var _ store.AppUpdateDocumentStore = (*AppUpdateDocumentStore)(nil)

func NewAppUpdateDocumentStore() *AppUpdateDocumentStore {
	return &AppUpdateDocumentStore{docs: make(map[string]domain.AppUpdateDocument)}
}

func (s *AppUpdateDocumentStore) AppUpdateDocument(_ context.Context, sha256 string) (domain.AppUpdateDocument, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, ok := s.docs[sha256]
	return doc, ok, nil
}

func (s *AppUpdateDocumentStore) PutAppUpdateDocument(_ context.Context, doc domain.AppUpdateDocument) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.docs[doc.SHA256]; ok {
		return nil
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now().UTC()
	}
	s.docs[doc.SHA256] = doc
	return nil
}
