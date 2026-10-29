package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

const pushDevicesAuthorizationFKConstraint = "push_devices_auth_key_id_fkey"

// PushDeviceStore 是 store.PushDeviceStore 的 PostgreSQL 实现（表 push_devices）。
// 表对 authorizations 设外键级联删除，删除授权的路径无需逐个处理登记。
type PushDeviceStore struct {
	q *sqlcgen.Queries
}

var _ store.PushDeviceStore = (*PushDeviceStore)(nil)

// NewPushDeviceStore 创建设备推送登记存储。
func NewPushDeviceStore(db sqlcgen.DBTX) *PushDeviceStore {
	return &PushDeviceStore{q: sqlcgen.New(db)}
}

func (s *PushDeviceStore) RegisterPushDevice(ctx context.Context, device domain.PushDevice) error {
	otherUIDs := device.OtherUIDs
	if otherUIDs == nil {
		otherUIDs = []int64{}
	}
	err := s.q.UpsertPushDevice(ctx, sqlcgen.UpsertPushDeviceParams{
		AuthKeyID:  authKeyIDToInt64(device.AuthKeyID),
		TokenType:  int32(device.TokenType),
		Token:      device.Token,
		UserID:     device.UserID,
		AppSandbox: device.AppSandbox,
		Secret:     nonNilBytea(device.Secret),
		NoMuted:    device.NoMuted,
		OtherUids:  otherUIDs,
		LangCode:   device.LangCode,
		// 只在插入新登记时生效，重新登记保留已存的开关（见 UpsertPushDevice）。
		PushEnabled: device.PushEnabled,
	})
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation && pgErr.ConstraintName == pushDevicesAuthorizationFKConstraint {
		return store.ErrPushDeviceAuthorizationNotFound
	}
	return fmt.Errorf("upsert push device: %w", err)
}

func (s *PushDeviceStore) ListPushDevicesByUser(ctx context.Context, userID int64) ([]domain.PushDevice, error) {
	rows, err := s.q.ListPushDevicesByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list push devices: %w", err)
	}
	out := make([]domain.PushDevice, 0, len(rows))
	for _, row := range rows {
		out = append(out, pushDeviceFromRow(row.PushDevice, row.AuthorizationUserID))
	}
	return out, nil
}

func (s *PushDeviceStore) ListPushDevicesByUsers(ctx context.Context, userIDs []int64) ([]domain.PushDevice, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListPushDevicesByUsers(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("list push devices by users: %w", err)
	}
	out := make([]domain.PushDevice, 0, len(rows))
	for _, row := range rows {
		out = append(out, pushDeviceFromRow(row.PushDevice, row.AuthorizationUserID))
	}
	return out, nil
}

func pushDeviceFromRow(row sqlcgen.PushDevice, authorizationUserID int64) domain.PushDevice {
	return domain.PushDevice{
		AuthKeyID:           authKeyIDFromInt64(row.AuthKeyID),
		UserID:              row.UserID,
		TokenType:           int(row.TokenType),
		Token:               row.Token,
		AppSandbox:          row.AppSandbox,
		Secret:              row.Secret,
		NoMuted:             row.NoMuted,
		OtherUIDs:           row.OtherUids,
		LangCode:            row.LangCode,
		PushEnabled:         row.PushEnabled,
		CreatedAt:           row.CreatedAt.Time,
		AuthorizationUserID: authorizationUserID,
	}
}

func (s *PushDeviceStore) DeletePushDevice(ctx context.Context, authKeyID [8]byte, tokenType int, token string) (bool, error) {
	n, err := s.q.DeletePushDevice(ctx, sqlcgen.DeletePushDeviceParams{
		AuthKeyID: authKeyIDToInt64(authKeyID),
		TokenType: int32(tokenType),
		Token:     token,
	})
	if err != nil {
		return false, fmt.Errorf("delete push device: %w", err)
	}
	return n > 0, nil
}

func (s *PushDeviceStore) DeleteStalePushDevice(ctx context.Context, device domain.PushDevice) (bool, error) {
	n, err := s.q.DeleteStalePushDevice(ctx, sqlcgen.DeleteStalePushDeviceParams{
		AuthKeyID: authKeyIDToInt64(device.AuthKeyID),
		TokenType: int32(device.TokenType),
		Token:     device.Token,
		UserID:    device.UserID,
	})
	if err != nil {
		return false, fmt.Errorf("delete stale push device: %w", err)
	}
	return n > 0, nil
}

func (s *PushDeviceStore) SetPushDevicesEnabled(ctx context.Context, userID int64, authKeyID [8]byte, enabled bool) (int, error) {
	n, err := s.q.SetPushDevicesEnabled(ctx, sqlcgen.SetPushDevicesEnabledParams{
		PushEnabled: enabled,
		AuthKeyID:   authKeyIDToInt64(authKeyID),
		UserID:      userID,
	})
	if err != nil {
		return 0, fmt.Errorf("set push devices enabled: %w", err)
	}
	return int(n), nil
}
