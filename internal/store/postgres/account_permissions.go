package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

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
	return accountPermissionsFromRow(row), true, nil
}

func accountPermissionsFromRow(row sqlcgen.GetAccountPermissionsRow) domain.AccountPermissions {
	whitelist := row.SendWhitelist
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
	}
}

func (s *AccountPermissionsStore) SetAccountPermissions(ctx context.Context, p domain.AccountPermissions) (domain.AccountPermissions, error) {
	if p.UserID <= 0 {
		return domain.AccountPermissions{}, domain.ErrUserNotFound
	}
	var written domain.AccountPermissions
	err := withTx(ctx, s.db, "set account permissions", func(tx pgx.Tx) error {
		var err error
		written, err = writeAccountPermissionsTx(ctx, tx, p)
		return err
	})
	if err != nil {
		return domain.AccountPermissions{}, accountPermissionsWriteErr("set account permissions", err)
	}
	return written, nil
}

func (s *AccountPermissionsStore) UpdateAccountPermissions(ctx context.Context, userID int64, merge store.AccountPermissionsMerge) (prev domain.AccountPermissions, prevFound bool, written domain.AccountPermissions, err error) {
	if userID <= 0 {
		return domain.AccountPermissions{}, false, domain.AccountPermissions{}, domain.ErrUserNotFound
	}
	err = withTx(ctx, s.db, "update account permissions", func(tx pgx.Tx) error {
		// 先拿账号级的 advisory 锁再锁权限行：还没有权限行时 FOR UPDATE 锁不住任何东西，两次
		// 首次设置要靠这把锁排队。
		if err := lockUsersForUpdate(ctx, tx, userID); err != nil {
			return err
		}
		row, err := sqlcgen.New(tx).LockAccountPermissions(ctx, userID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			prev, prevFound = domain.AccountPermissions{}, false
		case err != nil:
			return err
		default:
			prev, prevFound = accountPermissionsFromRow(sqlcgen.GetAccountPermissionsRow(row)), true
		}
		next := merge(clonePermissions(prev), prevFound)
		next.UserID = userID
		written, err = writeAccountPermissionsTx(ctx, tx, next)
		return err
	})
	if err != nil {
		return domain.AccountPermissions{}, false, domain.AccountPermissions{}, accountPermissionsWriteErr("update account permissions", err)
	}
	return prev, prevFound, written, nil
}

func clonePermissions(p domain.AccountPermissions) domain.AccountPermissions {
	p.SendWhitelist = slices.Clone(p.SendWhitelist)
	return p
}

// writeAccountPermissionsTx 整体替换权限行与名单：upsert 拿到权限行的行锁，随后的"删名单 + 写名单"
// 因此不会与别的提交穿插；同一事务里 bump read model，提交后通知各实例失效缓存。
func writeAccountPermissionsTx(ctx context.Context, tx pgx.Tx, p domain.AccountPermissions) (domain.AccountPermissions, error) {
	whitelist := domain.NormalizeSendWhitelist(p.SendWhitelist)
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
		return domain.AccountPermissions{}, err
	}
	p.UpdatedAt = updatedAt.Time
	if err := q.DeleteAccountSendWhitelist(ctx, p.UserID); err != nil {
		return domain.AccountPermissions{}, err
	}
	if len(whitelist) > 0 {
		if err := q.InsertAccountSendWhitelist(ctx, sqlcgen.InsertAccountSendWhitelistParams{UserID: p.UserID, MemberUserIds: whitelist}); err != nil {
			return domain.AccountPermissions{}, err
		}
	}
	if err := q.BumpAccountPermissionsReadModel(ctx, p.UserID); err != nil {
		return domain.AccountPermissions{}, err
	}
	p.SendWhitelist = whitelist
	return p, nil
}

func accountPermissionsWriteErr(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.ForeignKeyViolation {
		return domain.ErrUserNotFound
	}
	return fmt.Errorf("%s: %w", op, err)
}

// deleteAccountPermissionsTx 在账号注销事务里删除该账号的权限设置（名单随外键级联）、别的账号
// 白名单与默认权限模板白名单（#161）里指向它的条目，并通知失效受影响账号的权限视图。users 行是软删除，外键级联不会发生。
func deleteAccountPermissionsTx(ctx context.Context, tx pgx.Tx, userID int64) error {
	q := sqlcgen.New(tx)
	if err := q.DeleteAccountSendWhitelistByMember(ctx, userID); err != nil {
		return fmt.Errorf("delete whitelist entries for deleted account: %w", err)
	}
	if err := q.DeletePermissionDefaultsWhitelistMember(ctx, userID); err != nil {
		return fmt.Errorf("delete permission defaults whitelist entry for deleted account: %w", err)
	}
	if err := q.DeleteAccountPermissionsByUser(ctx, userID); err != nil {
		return fmt.Errorf("delete deleted account permissions: %w", err)
	}
	return nil
}
