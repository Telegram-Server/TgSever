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
	// 与 Bind / 2FA 转正拿同一把用户锁：正在进行的登录提交之后才写入，命令返回之后提交的
	// 登录一定看到新上限。
	err := withTx(ctx, s.db, "set device limit", func(tx pgx.Tx) error {
		if err := lockUsersForUpdate(ctx, tx, userID); err != nil {
			return fmt.Errorf("lock device limit user: %w", err)
		}
		return sqlcgen.New(tx).SetDeviceLimit(ctx, sqlcgen.SetDeviceLimitParams{UserID: userID, DeviceLimit: &value})
	})
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

// CheckDeviceLimit 是不加锁的只读预检，规则与绑定事务里的 enforceDeviceBinding 相同：
// 不看 password_pending（预检的是"完成登录之后"），不写绑定、不刷新最近使用时间。
func (s *AuthorizationStore) CheckDeviceLimit(ctx context.Context, userID int64, authKeyID [8]byte) error {
	deviceID, err := authKeyDeviceID(ctx, s.q, authKeyIDToInt64(authKeyID))
	if err != nil {
		return err
	}
	return enforceDeviceBinding(ctx, s.q, deviceBindingCandidate{UserID: userID, DeviceID: deviceID}, false)
}

// ListDeviceBindings 按绑定时间先后（同一时刻按 device_id）返回账号的设备绑定名单。
func (s *AuthorizationStore) ListDeviceBindings(ctx context.Context, userID int64) ([]domain.DeviceBinding, error) {
	rows, err := s.q.ListDeviceBindings(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list device bindings: %w", err)
	}
	out := make([]domain.DeviceBinding, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.DeviceBinding{
			UserID:      row.UserID,
			DeviceID:    row.DeviceID,
			DeviceModel: row.DeviceModel,
			Platform:    row.Platform,
			AppVersion:  row.AppVersion,
			BoundAt:     row.BoundAt.Time,
			LastUsedAt:  row.LastUsedAt.Time,
		})
	}
	return out, nil
}

// deviceBindingCandidate 是一次"完成登录"要判定的设备：账号、auth key 上持久化的
// device_id（空串 = 未上报），以及写入绑定时记录的设备信息。
type deviceBindingCandidate struct {
	UserID      int64
	DeviceID    string
	DeviceModel string
	Platform    string
	AppVersion  string
}

// authKeyDeviceID 读 auth key 上持久化的 device_id；key 不存在或从未上报都返回空串。
func authKeyDeviceID(ctx context.Context, q *sqlcgen.Queries, authKeyID int64) (string, error) {
	deviceID, err := q.GetAuthKeyDeviceID(ctx, authKeyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("read auth key device id: %w", err)
	}
	return deviceID, nil
}

// enforceDeviceBinding 对一次"完成登录"执行绑定规则（见 store.DeviceLimitStore；password_pending
// 由调用方跳过）：bot 或没有上限 → 放行；device_id 不合法 → domain.ErrDeviceIDRequired；
// 已绑定 → 放行（write 时刷新最近使用时间）；绑定数 < 上限 → 放行（write 时写入绑定）；
// 否则 domain.ErrDeviceLimitReached。
//
// write=true 时调用方必须已在同一事务里拿到该账号的用户锁（lockUsersForUpdate）：同一账号的
// 绑定与转正都先拿这把锁，"计数 + 插入"不会被并发登录穿插，绑定数永不超过上限。拒绝时
// 本函数不写任何行，调用方回滚或按自己的做法收尾。
func enforceDeviceBinding(ctx context.Context, q *sqlcgen.Queries, c deviceBindingCandidate, write bool) error {
	limit, err := q.GetEnforcedDeviceLimit(ctx, c.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read device limit: %w", err)
	}
	if limit == nil {
		return nil
	}
	if !domain.ValidDeviceID(c.DeviceID) {
		return domain.ErrDeviceIDRequired
	}
	if write {
		touched, err := q.TouchDeviceBinding(ctx, sqlcgen.TouchDeviceBindingParams{UserID: c.UserID, DeviceID: c.DeviceID})
		if err != nil {
			return fmt.Errorf("touch device binding: %w", err)
		}
		if touched > 0 {
			return nil
		}
	} else {
		bound, err := q.DeviceBindingExists(ctx, sqlcgen.DeviceBindingExistsParams{UserID: c.UserID, DeviceID: c.DeviceID})
		if err != nil {
			return fmt.Errorf("read device binding: %w", err)
		}
		if bound {
			return nil
		}
	}
	used, err := q.CountDeviceBindings(ctx, c.UserID)
	if err != nil {
		return fmt.Errorf("count device bindings: %w", err)
	}
	if used >= int64(*limit) {
		return domain.ErrDeviceLimitReached
	}
	if !write {
		return nil
	}
	if err := q.InsertDeviceBinding(ctx, sqlcgen.InsertDeviceBindingParams{
		UserID:      c.UserID,
		DeviceID:    c.DeviceID,
		DeviceModel: c.DeviceModel,
		Platform:    c.Platform,
		AppVersion:  c.AppVersion,
	}); err != nil {
		return fmt.Errorf("insert device binding: %w", err)
	}
	return nil
}
