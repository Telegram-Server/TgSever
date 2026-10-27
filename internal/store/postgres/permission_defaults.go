package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// 默认权限模板（#161）的 postgres 实现挂在 AccountPermissionsStore 上：account_permission_defaults
// 唯一的一行 + account_permission_defaults_whitelist 名单。账号注销经 deleteAccountPermissionsTx
// 在注销事务里一并从模板白名单中删除。模板不进权限视图缓存，所以写入不 bump read model。

var _ store.PermissionDefaultsStore = (*AccountPermissionsStore)(nil)

func (s *AccountPermissionsStore) GetPermissionDefaults(ctx context.Context) (domain.PermissionDefaults, bool, error) {
	row, err := s.q.GetPermissionDefaults(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PermissionDefaults{}, false, nil
		}
		return domain.PermissionDefaults{}, false, fmt.Errorf("get permission defaults: %w", err)
	}
	return permissionDefaultsFromRow(row), true, nil
}

func permissionDefaultsFromRow(row sqlcgen.GetPermissionDefaultsRow) domain.PermissionDefaults {
	whitelist := row.SendWhitelist
	if whitelist == nil {
		whitelist = []int64{}
	}
	return domain.PermissionDefaults{
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
		ApplyToSelfSignup:    row.ApplyToSelfSignup,
		UpdatedAt:            row.UpdatedAt.Time,
		CommandID:            row.CommandID,
	}
}

func (s *AccountPermissionsStore) UpdatePermissionDefaults(ctx context.Context, merge store.PermissionDefaultsMerge) (prev domain.PermissionDefaults, prevFound bool, written domain.PermissionDefaults, err error) {
	err = withTx(ctx, s.db, "update permission defaults", func(tx pgx.Tx) error {
		q := sqlcgen.New(tx)
		// 表锁让并发的合并更新排队（含两次首次设置）；之后读到的就是锁内的当前值。
		if err := q.LockPermissionDefaultsTable(ctx); err != nil {
			return err
		}
		row, err := q.GetPermissionDefaults(ctx)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			prev, prevFound = domain.PermissionDefaults{}, false
		case err != nil:
			return err
		default:
			prev, prevFound = permissionDefaultsFromRow(row), true
		}
		cur := prev
		cur.SendWhitelist = slices.Clone(prev.SendWhitelist)
		next := merge(cur, prevFound)
		// FOR SHARE 锁住成员行，与注销互斥：先提交的注销在这里被排除，后到的注销等这次提交后
		// 再删指向自己的条目。
		next.SendWhitelist, err = activeSendWhitelistMembersTx(ctx, q, next.SendWhitelist)
		if err != nil {
			return err
		}
		f := next.Flags
		updatedAt, err := q.UpsertPermissionDefaults(ctx, sqlcgen.UpsertPermissionDefaultsParams{
			Enabled:              next.Enabled,
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
			SendWhitelistEnabled: next.SendWhitelistEnabled,
			ApplyToSelfSignup:    next.ApplyToSelfSignup,
			CommandID:            next.CommandID,
		})
		if err != nil {
			return err
		}
		next.UpdatedAt = updatedAt.Time
		if err := q.DeletePermissionDefaultsWhitelist(ctx); err != nil {
			return err
		}
		if len(next.SendWhitelist) > 0 {
			if err := q.InsertPermissionDefaultsWhitelist(ctx, next.SendWhitelist); err != nil {
				return err
			}
		}
		written = next
		return nil
	})
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return domain.PermissionDefaults{}, false, domain.PermissionDefaults{}, err
		}
		return domain.PermissionDefaults{}, false, domain.PermissionDefaults{}, accountPermissionsWriteErr("update permission defaults", err)
	}
	return prev, prevFound, written, nil
}
