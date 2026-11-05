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
	// Guest 非 nil 时把新账号记为游客账号，并把它的 IP 键绑定到新账号（见 GuestAccountExtras）。
	Guest *GuestAccountExtras
}

// GuestAccountExtras 是游客注册建号时一起写入的游客标记与 IP 绑定。
type GuestAccountExtras struct {
	// IPKey 是注册请求的 IP 键（domain 层规范化过）。
	IPKey string
	// PreviousUserID 是调用方读到的该 IP 键当前绑定的账号；nil 表示读到时没有绑定。绑定只在
	// 仍是这个值时才换成新账号（比较并交换），否则返回 domain.ErrGuestIPBindingChanged 并整体回滚。
	PreviousUserID *int64
}

// GuestAccountStore 读取游客标记与 IP 绑定。写入只经 AccountCreationStore.CreateAccount
// （AccountExtras.Guest），与建号在同一个事务里。
type GuestAccountStore interface {
	// GuestIPBinding 读取 IP 键当前的绑定；没有时 found=false。
	GuestIPBinding(ctx context.Context, ipKey string) (domain.GuestIPBinding, bool, error)
	// GuestAccount 读取账号的游客标记；不是游客账号时 found=false。
	GuestAccount(ctx context.Context, userID int64) (domain.GuestAccount, bool, error)
}

// AccountCreationStore 是通用的建号操作：建号与附加项在同一个事务里完成，任何一步失败整体
// 回滚，不留下账号。memory 与 postgres 实现同一份契约（storetest.AccountCreationContract）。
type AccountCreationStore interface {
	// CreateAccount 建号并写入 extras 里给出的附加项，返回建好的账号。号码已被占用返回
	// domain.ErrPhoneNumberOccupied，用户名已被占用返回 domain.ErrUsernameOccupied。
	CreateAccount(ctx context.Context, u domain.User, extras AccountExtras) (domain.User, error)
}
