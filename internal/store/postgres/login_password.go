package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

const (
	loginPasswordsUserFKConstraint   = "login_passwords_user_id_fkey"
	loginPasswordsMaterialConstraint = "login_passwords_material_check"
)

// LoginPasswordStore 是 store.LoginPasswordStore 的 PostgreSQL 实现（表 login_passwords）。
type LoginPasswordStore struct {
	q *sqlcgen.Queries
}

var _ store.LoginPasswordStore = (*LoginPasswordStore)(nil)

// NewLoginPasswordStore 创建登录密码存储。建号时一起写入登录密码见 AccountCreationStore。
func NewLoginPasswordStore(db sqlcgen.DBTX) *LoginPasswordStore {
	return &LoginPasswordStore{q: sqlcgen.New(db)}
}

func (s *LoginPasswordStore) GetLoginPassword(ctx context.Context, userID int64) (domain.LoginPassword, bool, error) {
	row, err := s.q.GetLoginPassword(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.LoginPassword{}, false, nil
		}
		return domain.LoginPassword{}, false, fmt.Errorf("get login password: %w", err)
	}
	return domain.LoginPassword{
		Salt1:     row.Salt1,
		Salt2:     row.Salt2,
		Verifier:  row.Verifier,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}, true, nil
}

func (s *LoginPasswordStore) SetLoginPassword(ctx context.Context, userID int64, password domain.LoginPassword) error {
	return upsertLoginPassword(ctx, s.q, userID, password)
}

func (s *LoginPasswordStore) DeleteLoginPassword(ctx context.Context, userID int64) (bool, error) {
	n, err := s.q.DeleteLoginPassword(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("delete login password: %w", err)
	}
	return n > 0, nil
}

// upsertLoginPassword 写入或替换登录密码，并把约束冲突映射成存储层错误：
// 外键冲突（账号不存在）→ domain.ErrUserNotFound，材料检查失败 → store.ErrLoginPasswordInvalid。
// 空字节串按空 bytea 而不是 NULL 写入，让 login_passwords_material_check 统一兜住缺失的材料。
func upsertLoginPassword(ctx context.Context, q *sqlcgen.Queries, userID int64, password domain.LoginPassword) error {
	err := q.UpsertLoginPassword(ctx, sqlcgen.UpsertLoginPasswordParams{
		UserID:   userID,
		Salt1:    nonNilBytea(password.Salt1),
		Salt2:    nonNilBytea(password.Salt2),
		Verifier: nonNilBytea(password.Verifier),
	})
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == pgerrcode.ForeignKeyViolation && pgErr.ConstraintName == loginPasswordsUserFKConstraint:
			return domain.ErrUserNotFound
		case pgErr.Code == pgerrcode.CheckViolation && pgErr.ConstraintName == loginPasswordsMaterialConstraint:
			return store.ErrLoginPasswordInvalid
		}
	}
	return fmt.Errorf("upsert login password: %w", err)
}
