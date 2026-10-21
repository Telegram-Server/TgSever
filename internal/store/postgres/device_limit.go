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

// 账号设备上限（#105）：存在 account_restrictions.device_limit（NULL = 不限制），由
// AuthorizationStore 读写，与绑定事务里的计数用同一个 store。

var _ store.DeviceLimitStore = (*AuthorizationStore)(nil)

func (s *AuthorizationStore) GetDeviceLimit(ctx context.Context, userID int64) (int, bool, error) {
	limit, err := s.q.GetDeviceLimit(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("get device limit: %w", err)
	}
	if limit == nil {
		return 0, false, nil
	}
	return int(*limit), true, nil
}

func (s *AuthorizationStore) SetDeviceLimit(ctx context.Context, userID int64, limit int) error {
	if !domain.ValidDeviceLimit(limit) {
		return domain.ErrDeviceLimitInvalid
	}
	if userID <= 0 {
		return domain.ErrUserNotFound
	}
	value := int32(limit)
	err := s.q.SetDeviceLimit(ctx, sqlcgen.SetDeviceLimitParams{UserID: userID, DeviceLimit: &value})
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
		return domain.ErrUserNotFound
	}
	return fmt.Errorf("set device limit: %w", err)
}

func (s *AuthorizationStore) ClearDeviceLimit(ctx context.Context, userID int64) (bool, error) {
	n, err := s.q.ClearDeviceLimit(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("clear device limit: %w", err)
	}
	return n > 0, nil
}

// CheckDeviceLimit 是不加锁的只读预检，规则与绑定事务里的 checkDeviceLimit 相同。
func (s *AuthorizationStore) CheckDeviceLimit(ctx context.Context, userID int64, authKeyID [8]byte) error {
	return checkDeviceLimit(ctx, s.q, userID, authKeyIDToInt64(authKeyID))
}

// checkDeviceLimit 在绑定事务里、用户行已加锁之后执行：账号有上限（bot 不受限）且已完成
// 登录的其它授权数达到上限时返回 domain.ErrDeviceLimitReached。同一用户的绑定都先拿用户锁，
// 所以"计数 + 插入"不会被并发绑定穿插。
func checkDeviceLimit(ctx context.Context, q *sqlcgen.Queries, userID, authKeyID int64) error {
	limit, err := q.GetEnforcedDeviceLimit(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read device limit: %w", err)
	}
	if limit == nil {
		return nil
	}
	used, err := q.CountOtherCompletedAuthorizations(ctx, sqlcgen.CountOtherCompletedAuthorizationsParams{UserID: userID, AuthKeyID: authKeyID})
	if err != nil {
		return fmt.Errorf("count authorizations for device limit: %w", err)
	}
	if used >= int64(*limit) {
		return domain.ErrDeviceLimitReached
	}
	return nil
}
