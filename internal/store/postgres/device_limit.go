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

// SetDeviceLimit 写上限，并在同一事务、同一把用户锁下补绑（#115），返回补绑数。
func (s *AuthorizationStore) SetDeviceLimit(ctx context.Context, userID int64, limit int) (int, error) {
	if !domain.ValidDeviceLimit(limit) {
		return 0, domain.ErrDeviceLimitInvalid
	}
	if userID <= 0 {
		return 0, domain.ErrUserNotFound
	}
	value := int32(limit)
	backfilled := 0
	// 与 Bind / 2FA 转正拿同一把用户锁：正在进行的登录提交之后才写入，命令返回之后提交的
	// 登录一定看到新上限；补绑的"计数 + 插入"也不会被并发登录穿插。
	err := withTx(ctx, s.db, "set device limit", func(tx pgx.Tx) error {
		backfilled = 0
		if err := lockUsersForUpdate(ctx, tx, userID); err != nil {
			return fmt.Errorf("lock device limit user: %w", err)
		}
		q := sqlcgen.New(tx)
		if err := q.SetDeviceLimit(ctx, sqlcgen.SetDeviceLimitParams{UserID: userID, DeviceLimit: &value}); err != nil {
			return err
		}
		n, err := backfillDeviceBindings(ctx, q, userID, true)
		if err != nil {
			return err
		}
		backfilled = n
		return nil
	})
	if err == nil {
		return backfilled, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
		return 0, domain.ErrUserNotFound
	}
	return 0, fmt.Errorf("set device limit: %w", err)
}

// PreviewDeviceLimitBackfill 不加锁地计算把上限设为 limit 时会补绑的设备数（试运行）。
func (s *AuthorizationStore) PreviewDeviceLimitBackfill(ctx context.Context, userID int64, limit int) (int, error) {
	if !domain.ValidDeviceLimit(limit) {
		return 0, domain.ErrDeviceLimitInvalid
	}
	bot, err := s.q.IsBotUser(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("read device limit user: %w", err)
	}
	if bot {
		return 0, nil
	}
	plan, err := planDeviceBackfill(ctx, s.q, userID, limit)
	if err != nil {
		return 0, err
	}
	return len(plan), nil
}

// BackfillDeviceBinding 是延迟补绑（#115），见 store.DeviceLimitStore。先用一条不加锁的查询
// 排除未受限的账号（每次持久化 device_id 都会走到这里），再在用户锁下重新判定并写入。
func (s *AuthorizationStore) BackfillDeviceBinding(ctx context.Context, authKeyID [8]byte) (bool, error) {
	keyID := authKeyIDToInt64(authKeyID)
	target, err := s.q.GetDeviceBackfillTarget(ctx, keyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("read device backfill target: %w", err)
	}
	if !domain.ValidDeviceID(target.DeviceID) {
		return false, nil
	}
	bound := false
	err = withTx(ctx, s.db, "backfill device binding", func(tx pgx.Tx) error {
		bound = false
		if err := lockUsersForUpdate(ctx, tx, target.UserID); err != nil {
			return fmt.Errorf("lock device binding user: %w", err)
		}
		q := sqlcgen.New(tx)
		// 锁下重读：授权可能已被撤销或转给别的状态，上限可能已被清除。
		current, err := q.GetDeviceBackfillTarget(ctx, keyID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("read device backfill target: %w", err)
		}
		if current.UserID != target.UserID || !domain.ValidDeviceID(current.DeviceID) {
			return nil
		}
		ok, err := bindIfSlotFree(ctx, q, deviceBindingCandidate{
			UserID:      current.UserID,
			DeviceID:    current.DeviceID,
			DeviceModel: current.DeviceModel,
			Platform:    current.Platform,
			AppVersion:  current.AppVersion,
		})
		bound = ok
		return err
	})
	if err != nil {
		return false, fmt.Errorf("backfill device binding: %w", err)
	}
	return bound, nil
}

// bindIfSlotFree 在调用方已拿到用户锁的事务里：c 尚未绑定且绑定数 < 上限时写入绑定并返回
// true；没有上限（或 bot）、已绑定、名额已满都不写，返回 false。已绑定的设备不刷新最近使用时间。
func bindIfSlotFree(ctx context.Context, q *sqlcgen.Queries, c deviceBindingCandidate) (bool, error) {
	limit, err := q.GetEnforcedDeviceLimit(ctx, c.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("read device limit: %w", err)
	}
	if limit == nil {
		return false, nil
	}
	exists, err := q.DeviceBindingExists(ctx, sqlcgen.DeviceBindingExistsParams{UserID: c.UserID, DeviceID: c.DeviceID})
	if err != nil {
		return false, fmt.Errorf("read device binding: %w", err)
	}
	if exists {
		return false, nil
	}
	used, err := q.CountDeviceBindings(ctx, c.UserID)
	if err != nil {
		return false, fmt.Errorf("count device bindings: %w", err)
	}
	if used >= int64(*limit) {
		return false, nil
	}
	if err := q.InsertDeviceBinding(ctx, sqlcgen.InsertDeviceBindingParams{
		UserID:      c.UserID,
		DeviceID:    c.DeviceID,
		DeviceModel: c.DeviceModel,
		Platform:    c.Platform,
		AppVersion:  c.AppVersion,
	}); err != nil {
		return false, fmt.Errorf("insert device binding: %w", err)
	}
	return true, nil
}

// backfillDeviceBindings 按 planDeviceBackfill 的结果补绑到账号当前生效的上限（bot 或没有
// 上限时不补）。write=true 时调用方必须已在同一事务里拿到用户锁。
func backfillDeviceBindings(ctx context.Context, q *sqlcgen.Queries, userID int64, write bool) (int, error) {
	limit, err := q.GetEnforcedDeviceLimit(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("read device limit: %w", err)
	}
	if limit == nil {
		return 0, nil
	}
	plan, err := planDeviceBackfill(ctx, q, userID, int(*limit))
	if err != nil || !write {
		return len(plan), err
	}
	for _, c := range plan {
		if err := q.InsertDeviceBinding(ctx, sqlcgen.InsertDeviceBindingParams{
			UserID:      c.UserID,
			DeviceID:    c.DeviceID,
			DeviceModel: c.DeviceModel,
			Platform:    c.Platform,
			AppVersion:  c.AppVersion,
		}); err != nil {
			return 0, fmt.Errorf("insert backfilled device binding: %w", err)
		}
	}
	return len(plan), nil
}

// planDeviceBackfill 按登录时间先后挑出要补绑的设备：已完成授权的 auth key 上有合法 device_id、
// 尚未绑定、同一 device_id 只取一次，直到"已绑定 + 补绑"达到 limit。
func planDeviceBackfill(ctx context.Context, q *sqlcgen.Queries, userID int64, limit int) ([]deviceBindingCandidate, error) {
	boundIDs, err := q.ListBoundDeviceIDs(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list bound device ids: %w", err)
	}
	if len(boundIDs) >= limit {
		return nil, nil
	}
	taken := make(map[string]struct{}, len(boundIDs))
	for _, id := range boundIDs {
		taken[id] = struct{}{}
	}
	rows, err := q.ListDeviceBackfillCandidates(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list device backfill candidates: %w", err)
	}
	var plan []deviceBindingCandidate
	for _, row := range rows {
		if len(taken) >= limit {
			break
		}
		if !domain.ValidDeviceID(row.DeviceID) {
			continue
		}
		if _, ok := taken[row.DeviceID]; ok {
			continue
		}
		taken[row.DeviceID] = struct{}{}
		plan = append(plan, deviceBindingCandidate{
			UserID:      userID,
			DeviceID:    row.DeviceID,
			DeviceModel: row.DeviceModel,
			Platform:    row.Platform,
			AppVersion:  row.AppVersion,
		})
	}
	return plan, nil
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

// UnbindDevice 是后台解绑（#114），见 store.DeviceLimitStore。删绑定与撤销授权在同一事务里：
// 先拿用户锁（与 Bind / 2FA 转正 / 设上限同一把，锁序也与 Bind 相同：用户锁 → auth identity
// → auth_keys 行），解绑期间这台设备不会被同一账号重新绑定；撤销沿用 revokeByUserExceptTx 的
// 做法，只删业务授权与 update state，保留协议 key，客户端重连后收到 401。事务失败时什么都
// 没删，重试安全。
func (s *AuthorizationStore) UnbindDevice(ctx context.Context, userID int64, deviceID string) ([]domain.Authorization, error) {
	var out []domain.Authorization
	err := withAuthIdentityTx(ctx, s.db, "unbind device", func(tx pgx.Tx) error {
		out = nil
		if err := lockUsersForUpdate(ctx, tx, userID); err != nil {
			return fmt.Errorf("lock device binding user: %w", err)
		}
		q := sqlcgen.New(tx)
		n, err := q.DeleteDeviceBinding(ctx, sqlcgen.DeleteDeviceBindingParams{UserID: userID, DeviceID: deviceID})
		if err != nil {
			return fmt.Errorf("delete device binding: %w", err)
		}
		if n == 0 {
			return domain.ErrDeviceBindingNotFound
		}
		out, err = revokeAuthorizationsOnDeviceTx(ctx, tx, q, userID, deviceID)
		return err
	})
	if err != nil {
		if errors.Is(err, domain.ErrDeviceBindingNotFound) {
			return nil, domain.ErrDeviceBindingNotFound
		}
		return nil, fmt.Errorf("unbind device: %w", err)
	}
	return out, nil
}

// revokeAuthorizationsOnDeviceTx 撤销 userID 在 device_id 为 deviceID 的 auth key 上的全部授权。
// 候选查询只是提示：锁住 auth identity 与 auth_keys 行之后用新的语句快照重读，等待期间换了主
// 的 key 不会被误删。
func revokeAuthorizationsOnDeviceTx(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, userID int64, deviceID string) ([]domain.Authorization, error) {
	candidates, err := q.ListAuthKeyIDsOnDevice(ctx, sqlcgen.ListAuthKeyIDsOnDeviceParams{UserID: userID, DeviceID: &deviceID})
	if err != nil {
		return nil, fmt.Errorf("select unbind revoke candidates: %w", err)
	}
	if len(candidates) == 0 {
		return []domain.Authorization{}, nil
	}
	if err := lockPermanentAuthIdentities(ctx, tx, candidates); err != nil {
		return nil, err
	}
	if _, err := q.LockAuthKeysForUpdate(ctx, candidates); err != nil {
		return nil, fmt.Errorf("lock unbind auth keys: %w", err)
	}
	rows, err := q.ListAuthorizationsOnDeviceForUpdate(ctx, sqlcgen.ListAuthorizationsOnDeviceForUpdateParams{
		UserID: userID, DeviceID: &deviceID, AuthKeyIds: candidates,
	})
	if err != nil {
		return nil, fmt.Errorf("revalidate unbind authorizations: %w", err)
	}
	out := make([]domain.Authorization, 0, len(rows))
	targets := make([]int64, 0, len(rows))
	for _, row := range rows {
		a := authorizationFromRow(row.Authorization)
		a.PasswordPending = row.Authorization.PasswordPending
		out = append(out, a)
		targets = append(targets, row.Authorization.AuthKeyID)
	}
	if err := deleteRevokedAuthorizationStateTx(ctx, tx, targets); err != nil {
		return nil, err
	}
	return out, nil
}

// PreviewUnbindDevice 不加锁地报告解绑会撤销的授权数（试运行）。
func (s *AuthorizationStore) PreviewUnbindDevice(ctx context.Context, userID int64, deviceID string) (int, error) {
	exists, err := s.q.DeviceBindingExists(ctx, sqlcgen.DeviceBindingExistsParams{UserID: userID, DeviceID: deviceID})
	if err != nil {
		return 0, fmt.Errorf("read device binding: %w", err)
	}
	if !exists {
		return 0, domain.ErrDeviceBindingNotFound
	}
	n, err := s.q.CountAuthorizationsOnDevice(ctx, sqlcgen.CountAuthorizationsOnDeviceParams{UserID: userID, DeviceID: &deviceID})
	if err != nil {
		return 0, fmt.Errorf("count authorizations on device: %w", err)
	}
	return int(n), nil
}
