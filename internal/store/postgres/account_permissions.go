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

// AccountPermissionsStore 是员工权限设置（#119）的 postgres 实现：account_permissions 一行 +
// account_send_whitelist 名单。每次写入在同一事务里 bump read model "account_permissions"，
// 提交后 NOTIFY 让所有实例失效该账号的权限视图缓存。
type AccountPermissionsStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

var _ store.AccountPermissionsStore = (*AccountPermissionsStore)(nil)

// NewAccountPermissionsStore 基于 pgx 连接池创建权限设置 store。
func NewAccountPermissionsStore(db sqlcgen.DBTX) *AccountPermissionsStore {
	return &AccountPermissionsStore{db: db, q: sqlcgen.New(db)}
}

func (s *AccountPermissionsStore) GetAccountPermissions(ctx context.Context, userID int64) (domain.AccountPermissions, bool, error) {
	row, err := s.q.GetAccountPermissions(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.AccountPermissions{}, false, nil
		}
		return domain.AccountPermissions{}, false, fmt.Errorf("get account permissions: %w", err)
	}
	whitelist, err := s.q.ListAccountSendWhitelist(ctx, userID)
	if err != nil {
		return domain.AccountPermissions{}, false, fmt.Errorf("list account send whitelist: %w", err)
	}
	if whitelist == nil {
		whitelist = []int64{}
	}
	return domain.AccountPermissions{
		UserID:  row.UserID,
		Enabled: row.Enabled,
		Flags: domain.AccountPermissionFlags{
			Send:          row.AllowSend,
			Delete:        row.AllowDelete,
			Edit:          row.AllowEdit,
			ClearHistory:  row.AllowClearHistory,
			CreateGroup:   row.AllowCreateGroup,
			CreateChannel: row.AllowCreateChannel,
			JoinGroup:     row.AllowJoinGroup,
			JoinChannel:   row.AllowJoinChannel,
			LeaveGroup:    row.AllowLeaveGroup,
			LeaveChannel:  row.AllowLeaveChannel,
			AddContact:    row.AllowAddContact,
		},
		SendWhitelistEnabled: row.SendWhitelistEnabled,
		SendWhitelist:        whitelist,
		UpdatedAt:            row.UpdatedAt.Time,
		CommandID:            row.CommandID,
	}, true, nil
}

func (s *AccountPermissionsStore) SetAccountPermissions(ctx context.Context, p domain.AccountPermissions) (domain.AccountPermissions, error) {
	if p.UserID <= 0 {
		return domain.AccountPermissions{}, domain.ErrUserNotFound
	}
	whitelist := domain.NormalizeSendWhitelist(p.SendWhitelist)
	err := withTx(ctx, s.db, "set account permissions", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		f := p.Flags
		updatedAt, err := q.UpsertAccountPermissions(ctx, sqlcgen.UpsertAccountPermissionsParams{
			UserID:               p.UserID,
			Enabled:              p.Enabled,
			AllowSend:            f.Send,
			AllowDelete:          f.Delete,
			AllowEdit:            f.Edit,
			AllowClearHistory:    f.ClearHistory,
			AllowCreateGroup:     f.CreateGroup,
			AllowCreateChannel:   f.CreateChannel,
			AllowJoinGroup:       f.JoinGroup,
			AllowJoinChannel:     f.JoinChannel,
			AllowLeaveGroup:      f.LeaveGroup,
			AllowLeaveChannel:    f.LeaveChannel,
			AllowAddContact:      f.AddContact,
			SendWhitelistEnabled: p.SendWhitelistEnabled,
			CommandID:            p.CommandID,
		})
		if err != nil {
			return err
		}
		p.UpdatedAt = updatedAt.Time
		if err := q.DeleteAccountSendWhitelist(ctx, p.UserID); err != nil {
			return err
		}
		if len(whitelist) > 0 {
			if err := q.InsertAccountSendWhitelist(ctx, sqlcgen.InsertAccountSendWhitelistParams{UserID: p.UserID, MemberUserIds: whitelist}); err != nil {
				return err
			}
		}
		return q.BumpAccountPermissionsReadModel(ctx, p.UserID)
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
			return domain.AccountPermissions{}, domain.ErrUserNotFound
		}
		return domain.AccountPermissions{}, fmt.Errorf("set account permissions: %w", err)
	}
	p.SendWhitelist = whitelist
	return p, nil
}

// deleteAccountPermissionsTx 在账号注销事务里删除该账号的权限设置（名单随外键级联）与别的账号
// 白名单里指向它的条目，并通知失效受影响账号的权限视图。users 行是软删除，外键级联不会发生。
func deleteAccountPermissionsTx(ctx context.Context, tx pgx.Tx, userID int64) error {
	q := sqlcgen.New(tx)
	if err := q.DeleteAccountSendWhitelistByMember(ctx, userID); err != nil {
		return fmt.Errorf("delete whitelist entries for deleted account: %w", err)
	}
	if err := q.DeleteAccountPermissionsByUser(ctx, userID); err != nil {
		return fmt.Errorf("delete deleted account permissions: %w", err)
	}
	return nil
}
