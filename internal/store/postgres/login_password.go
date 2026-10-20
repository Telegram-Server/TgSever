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
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

var _ store.LoginPasswordStore = (*LoginPasswordStore)(nil)

// NewLoginPasswordStore 创建登录密码存储。db 必须支持事务（*pgxpool.Pool），
// CreateUserWithLoginPassword 需要在同一个事务里建号和写密码。
func NewLoginPasswordStore(db sqlcgen.DBTX) *LoginPasswordStore {
	return &LoginPasswordStore{db: db, q: sqlcgen.New(db)}
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

// CreateUserWithLoginPassword 复用建号事务主体 createUserTx，在同一个事务里写入登录密码后
// 一起提交。任何一步失败都回滚整个事务，不会留下没有密码的账号。
func (s *LoginPasswordStore) CreateUserWithLoginPassword(ctx context.Context, u domain.User, password domain.LoginPassword) (domain.User, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.User{}, fmt.Errorf("create user with login password: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("begin create user with login password: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	qtx := s.q.WithTx(tx)
	created, err := createUserTx(ctx, tx, qtx, u)
	if err != nil {
		return domain.User{}, err
	}
	if err := upsertLoginPassword(ctx, qtx, created.ID, password); err != nil {
		return domain.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.User{}, fmt.Errorf("commit create user with login password: %w", err)
	}
	committed = true
	return created, nil
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
