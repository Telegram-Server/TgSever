package store

import (
	"context"

	"telesrv/internal/domain"
)

// PermissionDefaultsStore 读写唯一的一份默认权限模板（#161）。没有模板行就表示没有模板。
// memory 与 postgres 实现同一份契约（storetest.PermissionDefaultsContract）。
//
// 账号注销（软删除）时，模板白名单里指向它的条目在注销事务里一起删除。
type PermissionDefaultsStore interface {
	// GetPermissionDefaults 返回模板；found=false 表示没有模板。白名单按用户 id 升序。
	GetPermissionDefaults(ctx context.Context) (d domain.PermissionDefaults, found bool, err error)
	// UpdatePermissionDefaults 在一个事务里锁住模板，把当前值（prev，没有模板时 prevFound=false）
	// 交给 merge 算出新模板并整体写回（白名单整份替换），UpdatedAt 记为写入时间，CommandID 原样
	// 保存。并发的两次更新按提交顺序排队，后一次的 merge 看到的是前一次写入后的值。merge 在锁内
	// 调用，不能做 I/O。白名单里的账号不存在时返回 domain.ErrUserNotFound，什么都不写；已注销的
	// 账号被排除（与注销互斥，不会留下指向已注销账号的条目）；重复项只保存一次。业务校验（上限、
	// 已注销）由调用方完成。
	UpdatePermissionDefaults(ctx context.Context, merge PermissionDefaultsMerge) (prev domain.PermissionDefaults, prevFound bool, written domain.PermissionDefaults, err error)
}

// PermissionDefaultsMerge 把当前模板（found=false 时为零值）合并成要写入的模板。
type PermissionDefaultsMerge func(current domain.PermissionDefaults, found bool) domain.PermissionDefaults
