package store

import (
	"context"

	"telesrv/internal/domain"
)

// AccountPermissionsStore 按账号读写员工权限设置（#118）：一行权限设置加一份发送白名单。
// memory 与 postgres 实现同一份契约（storetest.AccountPermissionsContract）。
//
// 账号注销（软删除）时，账号自己的权限设置与白名单、以及别的账号白名单里指向它的条目，
// 都在注销事务里一起删除。
type AccountPermissionsStore interface {
	// GetAccountPermissions 返回账号的权限设置；found=false 表示没有记录。白名单按用户 id 升序。
	GetAccountPermissions(ctx context.Context, userID int64) (p domain.AccountPermissions, found bool, err error)
	// SetAccountPermissions 整体替换 p.UserID 的权限行与全部白名单，在同一个事务里完成：并发
	// 提交时最终名单是其中某一次提交的完整名单，不会混合。UpdatedAt 由 store 记为写入时间，
	// CommandID 原样保存；返回写入后的设置。账号或白名单里的账号不存在时返回
	// domain.ErrUserNotFound，什么都不写。白名单的重复项只保存一次；业务校验（上限、已注销、
	// 含自己）由调用方完成。
	SetAccountPermissions(ctx context.Context, p domain.AccountPermissions) (domain.AccountPermissions, error)
}
