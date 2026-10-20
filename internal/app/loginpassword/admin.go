package loginpassword

import (
	"context"
	"crypto/rand"
	"fmt"
	"unicode/utf8"

	tdsrp "github.com/iamxvbaba/td/crypto/srp"

	"telesrv/internal/domain"
	"telesrv/internal/srp"
)

// 后台设置/重设登录密码（#88）：运营者输入明文密码，服务端替账号算出 verifier。不需要旧密码，
// 不管账号原来有没有登录密码都直接覆盖；不受密码登录总开关限制（总开关关闭时存下的密码只是
// 暂时用不上）。

// MaxPlaintextPasswordBytes 是后台设置登录密码时明文密码的最大长度（UTF-8 字节）。
//
// 客户端 SRP 本身没有长度上限：td（crypto/srp.NewHash）和 Telegram Desktop 设置云密码时都不限制
// 密码长度，PBKDF2 对任意长度的输入都能算。这里定 256 字节，是为了给服务端替运营者做的
// PBKDF2（10 万轮）和请求体一个确定的上界；256 字节远超任何人手输入的密码，客户端能设置的
// 密码在这个范围内都能由后台设置。
const MaxPlaintextPasswordBytes = 256

// NewLoginPasswordFromPlaintext 按客户端设置新密码的算法，从明文密码算出要保存的登录密码：
// 在默认 new_algo 的 salt1 后面追加 32 字节随机数，再用 td 客户端的 SRP 实现（与
// telegram/auth.NewPasswordHash 相同的 NewHash）算出 verifier。结果再经过与客户端提交时完全
// 相同的校验（newLoginPassword），保证和客户端设置的密码一样能登录。
//
// 密码至少 1 个字符、至多 MaxPlaintextPasswordBytes 字节，必须是合法的 UTF-8，否则返回
// domain.ErrLoginPasswordInvalid。
func NewLoginPasswordFromPlaintext(password string) (domain.LoginPassword, error) {
	if password == "" || len(password) > MaxPlaintextPasswordBytes || !utf8.ValidString(password) {
		return domain.LoginPassword{}, domain.ErrLoginPasswordInvalid
	}
	algo := srp.DefaultAlgo()
	hash, salt1, err := tdsrp.NewSRP(rand.Reader).NewHash([]byte(password), tdsrp.Input{
		Salt1: algo.Salt1,
		Salt2: algo.Salt2,
		G:     algo.G,
		P:     algo.P,
	})
	if err != nil {
		return domain.LoginPassword{}, fmt.Errorf("loginpassword: compute verifier: %w", err)
	}
	return newLoginPassword(domain.PasswordInputSettings{
		NewAlgo:         &domain.PasswordKDFAlgo{Salt1: salt1, Salt2: algo.Salt2, G: algo.G, P: algo.P},
		NewPasswordHash: hash,
	})
}

// CheckAdminLoginPassword 是后台设置登录密码的试运行：校验账号和密码，报告账号现在是否已有
// 登录密码，不写任何东西。系统账号、bot、已注销或不存在的账号返回
// auth.ErrSystemUserLoginForbidden；密码不合规返回 domain.ErrLoginPasswordInvalid。
func (s *Service) CheckAdminLoginPassword(ctx context.Context, userID int64, password string) (hasPassword bool, err error) {
	if err := s.adminReady(); err != nil {
		return false, err
	}
	if password == "" || len(password) > MaxPlaintextPasswordBytes || !utf8.ValidString(password) {
		return false, domain.ErrLoginPasswordInvalid
	}
	u, err := s.manageableUser(ctx, userID)
	if err != nil {
		return false, err
	}
	_, has, err := s.passwords.GetLoginPassword(ctx, u.ID)
	return has, err
}

// AdminSetLoginPassword 由后台给账号设置或重设登录密码，规则同 CheckAdminLoginPassword。
// 写入新 verifier 之后，之前签发的登录 challenge 记着旧 verifier，再交一律
// domain.ErrSRPPasswordChanged。成功后给账号发 777000 通知，文案注明由管理员设置或重设。
// hadPassword 报告账号原来是否已有登录密码。
func (s *Service) AdminSetLoginPassword(ctx context.Context, userID int64, password string) (hadPassword bool, err error) {
	if err := s.adminReady(); err != nil {
		return false, err
	}
	next, err := NewLoginPasswordFromPlaintext(password)
	if err != nil {
		return false, err
	}
	u, err := s.manageableUser(ctx, userID)
	if err != nil {
		return false, err
	}
	_, had, err := s.passwords.GetLoginPassword(ctx, u.ID)
	if err != nil {
		return false, err
	}
	if err := s.passwords.SetLoginPassword(ctx, u.ID, next); err != nil {
		return false, err
	}
	change := passwordAdminSet
	if had {
		change = passwordAdminReset
	}
	s.sendChangeNotice(ctx, u.ID, change)
	return had, nil
}

// adminReady 检查后台设置密码需要的依赖。与 Enabled 不同，不看密码登录总开关。
func (s *Service) adminReady() error {
	if s == nil || s.users == nil || s.passwords == nil {
		return fmt.Errorf("loginpassword: admin dependencies are not configured")
	}
	return nil
}
