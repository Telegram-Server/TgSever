package domain

import (
	"errors"
	"time"
)

// 手机号 + 密码登录（telesrv 扩展协议）的业务错误。
var (
	// ErrPasswordLoginDisabled 表示服务端总开关关闭，所有 telesrv. 密码登录方法一律拒绝。
	ErrPasswordLoginDisabled = errors.New("password login disabled")
	// ErrPasswordLoginNotSet 表示号码已有账号，但账号没有设置登录密码。
	ErrPasswordLoginNotSet = errors.New("password login not set")
	// ErrLoginPasswordInvalid 表示后台设置登录密码时给的明文密码不合规：为空、超过长度上限
	// 或不是合法的 UTF-8。
	ErrLoginPasswordInvalid = errors.New("login password invalid")
)

// LoginPassword 是账号的登录密码（与 2FA 云密码互相独立）。服务端只保存 SRP verifier，
// 从不保存明文或可逆形式的密码。Salt1 是设置密码时的 new_algo.salt1（服务端基础盐 +
// 客户端追加的 32 字节），Salt2 是 new_algo.salt2，Verifier 是 HashSize 定长的 v。
type LoginPassword struct {
	Salt1     []byte
	Salt2     []byte
	Verifier  []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}
