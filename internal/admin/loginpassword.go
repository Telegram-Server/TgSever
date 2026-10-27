package admin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

// 后台给已有账号设置或重设登录密码（#88）。走普通的命令通道：试运行、按 command id 幂等、写审计。
//
// 明文密码只在内存里经过：SetLoginPasswordRequest.Password 是 json:"-"，不进 RequestJSON、
// 命令日志和审计；结果 Details 只记"设置了 / 重设了"。幂等比较靠 PasswordFingerprint：由服务端
// 密钥派生的 HMAC，不知道密钥就无法拿一个候选密码离线比对；同一个 command id 换了密码再提交，
// 指纹不同，判为 COMMAND_ID_CONFLICT。

const ActionSetLoginPassword = "account.set_login_password"

// LoginPasswordSetter 是后台设置登录密码用到的登录密码服务（*loginpassword.Service）。
// 两个方法都不受密码登录总开关限制；系统账号、bot、已注销的账号返回
// auth.ErrSystemUserLoginForbidden，密码不合规返回 domain.ErrLoginPasswordInvalid。
type LoginPasswordSetter interface {
	// CheckAdminLoginPassword 是试运行：只校验，报告账号现在是否已有登录密码。
	CheckAdminLoginPassword(ctx context.Context, userID int64, password string) (hasPassword bool, err error)
	// AdminSetLoginPassword 覆盖写入新密码并给账号发 777000 通知。
	AdminSetLoginPassword(ctx context.Context, userID int64, password string) (hadPassword bool, err error)
	// ValidateAdminLoginPassword 是建号带密码（account.create）的试运行：只校验明文密码。
	ValidateAdminLoginPassword(password string) error
	// AdminAccountCreator 算出 verifier，返回"建号并写入登录密码"的单事务建号步骤，
	// 交给 AccountCreator.CreateAccountWith。permissions 非 nil 时同一个事务里一起写入权限（#162）。
	AdminAccountCreator(password string, permissions *domain.AccountPermissions) (auth.CreateAccountFunc, error)
}

// SetLoginPasswordRequest 设置或重设 UserID 的登录密码。RevokeSessions 为 true 时，设置成功后
// 撤销这个账号的全部授权（与 account.revoke_sessions 的 revoke_all 效果相同）。
type SetLoginPasswordRequest struct {
	CommandMeta
	UserID int64 `json:"user_id"`
	// Password 是明文密码，永远不序列化。
	Password string `json:"-"`
	// PasswordFingerprint 由 SetLoginPassword 根据 Password 填写（调用方传入的值会被覆盖），
	// 只用来让 RequestJSON 区分不同的密码，见 loginPasswordFingerprint。
	PasswordFingerprint string `json:"password_fingerprint"`
	RevokeSessions      bool   `json:"revoke_sessions,omitempty"`
}

// loginPasswordFingerprintDomain 是派生指纹密钥用的域分隔串，保证这把子密钥不会和服务端密钥的
// 其它用途撞在一起。
const loginPasswordFingerprintDomain = "telesrv/admin/login-password-fingerprint/v1"

// DeriveLoginPasswordFingerprintKey 由服务端密钥（生产中是 TELESRV_ADMIN_API_TOKEN）派生出
// 登录密码幂等指纹的 HMAC 子密钥：HMAC-SHA256(secret, 域分隔串)。secret 为空时返回 nil，
// 此时 SetLoginPassword 拒绝执行。
func DeriveLoginPasswordFingerprintKey(secret string) []byte {
	if secret == "" {
		return nil
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(loginPasswordFingerprintDomain))
	return mac.Sum(nil)
}

// loginPasswordFingerprint 是 HMAC-SHA256(key, user_id || password) 的十六进制。带上 user_id，
// 两个账号被设成同一个密码时命令记录里的指纹也不一样。
func loginPasswordFingerprint(key []byte, userID int64, password string) string {
	mac := hmac.New(sha256.New, key)
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], uint64(userID))
	mac.Write(id[:])
	mac.Write([]byte(password))
	return hex.EncodeToString(mac.Sum(nil))
}

// newAccountPasswordFingerprint 是建号带密码（account.create）的幂等指纹：
// HMAC-SHA256(key, 域前缀 || phone || 0 || password) 的十六进制。建号时还没有 user_id，用规范化后的
// 号码（domain.NormalizePhone，调用方负责）区分账号；域前缀保证它和 account.set_login_password 的指纹不会相同。
func newAccountPasswordFingerprint(key []byte, phone, password string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("account.create\x00"))
	mac.Write([]byte(phone))
	mac.Write([]byte{0})
	mac.Write([]byte(password))
	return hex.EncodeToString(mac.Sum(nil))
}

// SetLoginPassword 处理 account.set_login_password。试运行只校验账号和密码；执行时先写入新密码
// （之前签发的登录 challenge 随之作废）并发 777000 通知，再按 RevokeSessions 撤销全部授权。
func (s *Service) SetLoginPassword(ctx context.Context, req SetLoginPasswordRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.loginPasswords == nil {
		return CommandResult{}, fmt.Errorf("admin login password dependency is not configured")
	}
	if len(s.loginPasswordFingerprintKey) == 0 {
		return CommandResult{}, fmt.Errorf("admin login password fingerprint key is not configured")
	}
	if req.RevokeSessions && (s.auth == nil || s.revoker == nil) {
		return CommandResult{}, fmt.Errorf("admin auth dependencies are not configured")
	}
	req.PasswordFingerprint = loginPasswordFingerprint(s.loginPasswordFingerprintKey, req.UserID, req.Password)
	// 密码写入与 777000 通知无法回滚，所以命令不跟随请求取消（同 runServerIdentityCommand）：
	// 后台的请求在写入之后被掐断时，撤销会话与 FinishCommand 仍要做完，否则命令停在 running。
	ctx = context.WithoutCancel(ctx)
	return s.runCommand(ctx, req.CommandMeta, ActionSetLoginPassword, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"revoke_sessions": req.RevokeSessions}
		if req.DryRun {
			has, err := s.loginPasswords.CheckAdminLoginPassword(ctx, req.UserID, req.Password)
			if err != nil {
				return CommandResult{Details: details}, setLoginPasswordError(err)
			}
			details["had_password"] = has
			return CommandResult{Message: "login password validated", Details: details}, nil
		}
		had, err := s.loginPasswords.AdminSetLoginPassword(ctx, req.UserID, req.Password)
		if err != nil {
			return CommandResult{Details: details}, setLoginPasswordError(err)
		}
		details["had_password"] = had
		message := "login password set"
		if had {
			message = "login password reset"
		}
		if req.RevokeSessions {
			revoked, err := s.revokeAllAuthorizations(ctx, req.UserID, [8]byte{})
			details["revoked_hashes"] = authorizationHashes(revoked)
			details["revoked_count"] = len(revoked)
			if err != nil {
				// 密码已经写进去了（通知也已发出），命令按成功记录：记为失败会让运营者换个 command id
				// 重试，导致二次重设、二次通知。撤销失败写进消息与详情，提示另行撤销会话。
				details["revoke_sessions_failed"] = true
				details["revoke_sessions_error"] = err.Error()
				return CommandResult{Message: message + ", revoking sessions failed: revoke the sessions separately", Details: details}, nil
			}
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// setLoginPasswordError 给运营者能处理的拒绝加上稳定的错误码。
func setLoginPasswordError(err error) error {
	switch {
	case errors.Is(err, auth.ErrSystemUserLoginForbidden):
		return codedError("USER_INVALID", err)
	case errors.Is(err, domain.ErrLoginPasswordInvalid):
		return codedError("LOGIN_PASSWORD_INVALID", err)
	default:
		return err
	}
}
