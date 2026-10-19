package store

import (
	"context"
	"errors"

	"telesrv/internal/domain"
)

// ErrLoginPasswordInvalid 表示要写入的登录密码缺少 salt1、salt2 或 verifier。
var ErrLoginPasswordInvalid = errors.New("login password material missing")

// LoginPasswordStore 持久化账号的登录密码（手机号 + 密码登录），与 2FA 的 PasswordStore
// 互相独立。CreatedAt/UpdatedAt 由存储维护，写入时忽略调用方传入的值。
type LoginPasswordStore interface {
	// GetLoginPassword 读取账号的登录密码；没有设置时 found=false。
	GetLoginPassword(ctx context.Context, userID int64) (domain.LoginPassword, bool, error)
	// SetLoginPassword 设置或替换账号的登录密码。账号不存在返回 domain.ErrUserNotFound，
	// 密码材料不完整返回 ErrLoginPasswordInvalid。
	SetLoginPassword(ctx context.Context, userID int64, password domain.LoginPassword) error
	// DeleteLoginPassword 删除账号的登录密码，deleted 表示删除前确实存在。
	DeleteLoginPassword(ctx context.Context, userID int64) (deleted bool, err error)
	// CreateUserWithLoginPassword 在同一个事务里建号并写入登录密码：要么两者都生效，
	// 要么都不生效，不会出现"有账号没密码"的状态。号码已被占用返回
	// domain.ErrPhoneNumberOccupied，密码材料不完整返回 ErrLoginPasswordInvalid。
	CreateUserWithLoginPassword(ctx context.Context, u domain.User, password domain.LoginPassword) (domain.User, error)
}
