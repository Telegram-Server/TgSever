package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// AppUpdateDocumentStore 是 store.AppUpdateDocumentStore 的 postgres 实现。
type AppUpdateDocumentStore struct {
	q *sqlcgen.Queries
}

var _ store.AppUpdateDocumentStore = (*AppUpdateDocumentStore)(nil)

func NewAppUpdateDocumentStore(db sqlcgen.DBTX) *AppUpdateDocumentStore {
	return &AppUpdateDocumentStore{q: sqlcgen.New(db)}
}

func (s *AppUpdateDocumentStore) AppUpdateDocument(ctx context.Context, sha256 string) (domain.AppUpdateDocument, bool, error) {
	row, err := s.q.GetAppUpdateDocument(ctx, sha256)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AppUpdateDocument{}, false, nil
	}
	if err != nil {
		return domain.AppUpdateDocument{}, false, fmt.Errorf("get app update document: %w", err)
	}
	return domain.AppUpdateDocument{SHA256: row.Sha256, DocumentID: row.DocumentID, FileName: row.FileName, Size: row.Size, CreatedAt: row.CreatedAt.Time}, true, nil
}

func (s *AppUpdateDocumentStore) PutAppUpdateDocument(ctx context.Context, doc domain.AppUpdateDocument) error {
	if err := s.q.PutAppUpdateDocument(ctx, sqlcgen.PutAppUpdateDocumentParams{
		Sha256: doc.SHA256, DocumentID: doc.DocumentID, FileName: doc.FileName, Size: doc.Size,
	}); err != nil {
		return fmt.Errorf("put app update document: %w", err)
	}
	return nil
}
