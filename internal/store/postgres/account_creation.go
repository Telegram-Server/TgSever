package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// AccountCreationStore 是 store.AccountCreationStore 的 postgres 实现（#159）：建号（createUserTx）、
// 写登录密码（upsertLoginPassword）、写权限与白名单（writeAccountPermissionsTx，同一事务里 bump
// read model account_permissions，提交后 NOTIFY）在一个事务里完成，任何一步失败整体回滚。
type AccountCreationStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

var _ store.AccountCreationStore = (*AccountCreationStore)(nil)

// NewAccountCreationStore 创建建号操作。db 必须支持事务（*pgxpool.Pool）。
func NewAccountCreationStore(db sqlcgen.DBTX) *AccountCreationStore {
	return &AccountCreationStore{db: db, q: sqlcgen.New(db)}
}

func (s *AccountCreationStore) CreateAccount(ctx context.Context, u domain.User, extras store.AccountExtras) (domain.User, error) {
	var created domain.User
	err := withTx(ctx, s.db, "create account", func(tx pgx.Tx) error {
		qtx := s.q.WithTx(tx)
		var err error
		created, err = createUserTx(ctx, tx, qtx, u)
		if err != nil {
			return err
		}
		if extras.LoginPassword != nil {
			if err := upsertLoginPassword(ctx, qtx, created.ID, *extras.LoginPassword); err != nil {
				return err
			}
		}
		if extras.Permissions != nil {
			p := *extras.Permissions
			p.UserID = created.ID
			p.SendWhitelist, err = activeSendWhitelistMembersTx(ctx, qtx, p.SendWhitelist)
			if err != nil {
				return err
			}
			if _, err := writeAccountPermissionsTx(ctx, tx, p); err != nil {
				return accountPermissionsWriteErr("create account permissions", err)
			}
		}
		return nil
	})
	if err != nil {
		return domain.User{}, err
	}
	return created, nil
}

// activeSendWhitelistMembersTx 锁住白名单成员的账号行（与注销互斥，见 LockSendWhitelistMembers），
// 去掉已注销的成员；有成员不存在时返回 domain.ErrUserNotFound。
func activeSendWhitelistMembersTx(ctx context.Context, q *sqlcgen.Queries, members []int64) ([]int64, error) {
	members = domain.NormalizeSendWhitelist(members)
	if len(members) == 0 {
		return members, nil
	}
	rows, err := q.LockSendWhitelistMembers(ctx, members)
	if err != nil {
		return nil, fmt.Errorf("lock send whitelist members: %w", err)
	}
	if len(rows) != len(members) {
		return nil, domain.ErrUserNotFound
	}
	active := make([]int64, 0, len(rows))
	for _, row := range rows {
		if !row.Deleted {
			active = append(active, row.ID)
		}
	}
	return active, nil
}
