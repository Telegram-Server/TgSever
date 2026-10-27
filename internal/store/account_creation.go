package store

import (
	"context"

	"telesrv/internal/domain"
)

// AccountExtras 是建号时在同一个事务里一起写入的附加项，零值表示只建号。
type AccountExtras struct {
	// LoginPassword 非 nil 时写入登录密码；材料不完整返回 ErrLoginPasswordInvalid。
	LoginPassword *domain.LoginPassword
	// Permissions 非 nil 时写入权限设置与发送白名单（默认权限快照）。UserID 由 store 填为新账号的
	// id，UpdatedAt 记为写入时间；白名单里已注销的账号被排除，重复项只保存一次，不存在的账号
	// 返回 domain.ErrUserNotFound。写入时通知读模型 account_permissions。
	Permissions *domain.AccountPermissions
}

// AccountCreationStore 是通用的建号操作：建号与附加项在同一个事务里完成，任何一步失败整体
// 回滚，不留下账号。memory 与 postgres 实现同一份契约（storetest.AccountCreationContract）。
type AccountCreationStore interface {
	// CreateAccount 建号并写入 extras 里给出的附加项，返回建好的账号。号码已被占用返回
	// domain.ErrPhoneNumberOccupied，用户名已被占用返回 domain.ErrUsernameOccupied。
	CreateAccount(ctx context.Context, u domain.User, extras AccountExtras) (domain.User, error)
}
