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

// GuestAccountStore 是 store.GuestAccountStore 的 postgres 实现。写入在
// AccountCreationStore.CreateAccount 的事务里完成（writeGuestAccountTx）。
type GuestAccountStore struct {
	q *sqlcgen.Queries
}

var _ store.GuestAccountStore = (*GuestAccountStore)(nil)

func NewGuestAccountStore(db sqlcgen.DBTX) *GuestAccountStore {
	return &GuestAccountStore{q: sqlcgen.New(db)}
}

func (s *GuestAccountStore) GuestIPBinding(ctx context.Context, ipKey string) (domain.GuestIPBinding, bool, error) {
	row, err := s.q.GetGuestIPBinding(ctx, ipKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GuestIPBinding{}, false, nil
	}
	if err != nil {
		return domain.GuestIPBinding{}, false, fmt.Errorf("get guest ip binding: %w", err)
	}
	return domain.GuestIPBinding{IPKey: row.IpKey, UserID: row.UserID, BoundAt: row.BoundAt.Time}, true, nil
}

func (s *GuestAccountStore) GuestAccount(ctx context.Context, userID int64) (domain.GuestAccount, bool, error) {
	row, err := s.q.GetGuestAccount(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.GuestAccount{}, false, nil
	}
	if err != nil {
		return domain.GuestAccount{}, false, fmt.Errorf("get guest account: %w", err)
	}
	return domain.GuestAccount{UserID: row.UserID, Phone: row.Phone, IPKey: row.IpKey, CreatedAt: row.CreatedAt.Time}, true, nil
}

// writeGuestAccountTx 在建号事务里写游客标记，并按比较并交换把 IP 键绑定到新账号；
// 绑定已被别的请求换掉时返回 domain.ErrGuestIPBindingChanged，调用方回滚整个建号。
func writeGuestAccountTx(ctx context.Context, q *sqlcgen.Queries, created domain.User, guest store.GuestAccountExtras) error {
	if guest.IPKey == "" {
		return fmt.Errorf("create guest account: empty ip key")
	}
	if err := q.InsertGuestAccount(ctx, sqlcgen.InsertGuestAccountParams{UserID: created.ID, Phone: created.Phone, IpKey: guest.IPKey}); err != nil {
		return fmt.Errorf("insert guest account: %w", err)
	}
	rows, err := q.BindGuestIP(ctx, sqlcgen.BindGuestIPParams{IpKey: guest.IPKey, UserID: created.ID, PreviousUserID: guest.PreviousUserID})
	if err != nil {
		return fmt.Errorf("bind guest ip: %w", err)
	}
	if rows == 0 {
		return domain.ErrGuestIPBindingChanged
	}
	return nil
}
