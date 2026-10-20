package loginpassword

import (
	"bytes"
	"context"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/app/account"
	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/srp"
)

// 已登录时管理登录密码：telesrv.getMyLoginPassword 与 telesrv.updateLoginPassword。
// 规则与 2FA 云密码的 account.getPassword / account.updatePasswordSettings 一致：已有登录
// 密码时，改动必须带上当前密码的 SRP 证明（例外是忘记密码：刚登录的授权在重设窗口内可以
// 不带，见 WithResetWindow）；new_password_hash 为空表示删除。每次改动成功后由系统账号
// 777000 给这个账号发一条通知消息。

// NoticeSender 落一条私聊消息（store.MessageStore.SendPrivateText）。生产里是 postgres 的
// MessageStore：消息、pts 事件和 dispatch outbox 在同一个事务里提交，outbox 再把
// updateNewMessage 推给这个账号的所有在线会话，离线的会话重连后经 getDifference 补收。
// 这和注销账号前 777000 发出的确认消息走的是同一条路。
type NoticeSender interface {
	SendPrivateText(ctx context.Context, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error)
}

// WithChangeNotices 注入登录密码改动通知的发送方。未注入时不发通知。通知是尽力而为：
// 密码已经改好之后通知失败，只记日志，不把整个请求变成失败（客户端重试会因为密码已经
// 变了而得到误导性的错误）。
func WithChangeNotices(sender NoticeSender, log *zap.Logger) Option {
	return func(s *Service) {
		s.notices = sender
		s.log = log
	}
}

// WithResetWindow 设置忘记登录密码时免旧密码重设的窗口（TELESRV_PASSWORD_LOGIN_RESET_WINDOW）。
// 忘了登录密码的用户先用验证码登录，当前授权的创建时间在 window 内时，updateLoginPassword
// 的 current 可以为空。授权的创建时间每次登录（含完成 2FA）都会刷新，提现前的"刚登录"
// 校验用的也是它。window <=0（默认）关闭：已有登录密码时一律要求 current。
func WithResetWindow(window time.Duration) Option {
	return func(s *Service) {
		s.resetWindow = window
	}
}

// withinResetWindow 报告创建于 signedInAt 的授权是否还在重设窗口内。signedInAt 为零值
// （调用方没有读到授权）一律不算。
func (s *Service) withinResetWindow(signedInAt time.Time) bool {
	if s.resetWindow <= 0 || signedInAt.IsZero() {
		return false
	}
	return s.now().Sub(signedInAt) < s.resetWindow
}

// changeNoticeTimeout 是发送改动通知的上限，与请求本身的取消无关。
const changeNoticeTimeout = 5 * time.Second

// passwordChange 是一次成功改动的种类，决定 777000 通知的文案。
type passwordChange int

const (
	passwordSet passwordChange = iota + 1
	passwordChanged
	passwordRemoved
)

// GetMyLoginPassword 处理 telesrv.getMyLoginPassword（必须已登录）：
//   - 账号没有登录密码：has_password=false，带 new_algo 等设置密码用的参数；
//   - 账号有登录密码：has_password=true，带 current_algo、srp_B、srp_id。challenge 与登录用的
//     一样只有 rawAuthKeyID 能用、取出一次就作废、challengeTTL 后过期。
func (s *Service) GetMyLoginPassword(ctx context.Context, rawAuthKeyID [8]byte, userID int64) (domain.PasswordSettings, error) {
	if !s.Enabled() {
		return domain.PasswordSettings{}, domain.ErrPasswordLoginDisabled
	}
	u, err := s.manageableUser(ctx, userID)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	password, found, err := s.passwords.GetLoginPassword(ctx, u.ID)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	if !found {
		return account.NoPasswordSettings(), nil
	}
	if rawAuthKeyID == ([8]byte{}) {
		return domain.PasswordSettings{}, errNoAuthKey
	}
	ch, err := s.issueChallenge(ctx, u.ID, u.Phone, rawAuthKeyID, password)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	settings := account.NoPasswordSettings()
	defaults := srp.DefaultAlgo()
	settings.HasPassword = true
	settings.CurrentAlgo = &domain.PasswordKDFAlgo{
		Salt1: append([]byte(nil), password.Salt1...),
		Salt2: append([]byte(nil), password.Salt2...),
		G:     defaults.G,
		P:     defaults.P,
	}
	settings.SRPB = ch.B
	settings.SRPID = ch.SRPID
	return settings, nil
}

// UpdateMyLoginPassword 处理 telesrv.updateLoginPassword（必须已登录）：
//   - 先校验 new_settings（只读 new_algo 和 new_password_hash，带 email 拒绝，hint 忽略），
//     不合法的请求不会消耗 current 的 challenge；
//   - 账号已有登录密码：current 必须是 getMyLoginPassword（或同一 auth_key 上的
//     getLoginPassword）签发的 challenge 的正确证明，错误码与 signInWithPassword 相同：
//     challenge 不存在、过期、不是发给 rawAuthKeyID 或这个账号的 → domain.ErrSRPIDInvalid；
//     期间密码被改过 → domain.ErrSRPPasswordChanged；证明不成立 → domain.ErrPasswordHashInvalid；
//   - 忘记密码：signedInAt（当前授权的创建时间）在重设窗口内时（见 WithResetWindow），
//     current 可以为空；窗口外或窗口关闭时，已有登录密码而 current 为空 →
//     domain.ErrPasswordHashInvalid；
//   - 账号没有登录密码：current 必须是 inputCheckPasswordEmpty；
//   - new_password_hash 为空表示删除，否则设置或修改。成功后给账号发 777000 通知。
//
// 免旧密码的重设与普通修改走同一条路：同样发 777000 通知；之前签发的 challenge 记着旧
// verifier，之后再交一律 domain.ErrSRPPasswordChanged。
func (s *Service) UpdateMyLoginPassword(ctx context.Context, rawAuthKeyID [8]byte, userID int64, signedInAt time.Time, current domain.PasswordCheck, in domain.PasswordInputSettings) error {
	if !s.Enabled() {
		return domain.ErrPasswordLoginDisabled
	}
	u, err := s.manageableUser(ctx, userID)
	if err != nil {
		return err
	}
	var next *domain.LoginPassword
	if len(in.NewPasswordHash) > 0 || in.HasEmail {
		password, err := newLoginPassword(in)
		if err != nil {
			return err
		}
		next = &password
	}
	stored, has, err := s.passwords.GetLoginPassword(ctx, u.ID)
	if err != nil {
		return err
	}
	if err := s.checkCurrentLoginPassword(ctx, rawAuthKeyID, u.ID, stored, has, s.withinResetWindow(signedInAt), current); err != nil {
		return err
	}
	var change passwordChange
	switch {
	case next != nil:
		if err := s.passwords.SetLoginPassword(ctx, u.ID, *next); err != nil {
			return err
		}
		change = passwordChanged
		if !has {
			change = passwordSet
		}
	case has:
		deleted, err := s.passwords.DeleteLoginPassword(ctx, u.ID)
		if err != nil {
			return err
		}
		if !deleted {
			// 期间已被别的会话删掉：结果一致，那一次删除已经发过通知。
			return nil
		}
		change = passwordRemoved
	default:
		// 本来就没有登录密码，删除是空操作，没有改动可通知。
		return nil
	}
	s.sendChangeNotice(ctx, u.ID, change)
	return nil
}

// manageableUser 读取要管理登录密码的账号。系统账号、bot、已注销的账号一律拒绝，与密码
// 登录同一条规则（auth.ErrSystemUserLoginForbidden）。
func (s *Service) manageableUser(ctx context.Context, userID int64) (domain.User, error) {
	if userID == 0 {
		return domain.User{}, auth.ErrSystemUserLoginForbidden
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found || u.Deleted || u.Bot || domain.IsSystemUserID(u.ID) {
		return domain.User{}, auth.ErrSystemUserLoginForbidden
	}
	return u, nil
}

// checkCurrentLoginPassword 校验 updateLoginPassword 的 current。has/stored 是刚读出的登录密码；
// resetting 表示当前授权还在忘记密码的重设窗口内，此时允许 current 为空。
func (s *Service) checkCurrentLoginPassword(ctx context.Context, rawAuthKeyID [8]byte, userID int64, stored domain.LoginPassword, has, resetting bool, current domain.PasswordCheck) error {
	if current.Empty {
		if has && !resetting {
			return domain.ErrPasswordHashInvalid
		}
		return nil
	}
	if current.SRPID == 0 {
		return domain.ErrSRPIDInvalid
	}
	// 不论之后成败，challenge 都已作废。
	ch, found, err := s.challenges.TakeLoginPasswordChallenge(ctx, current.SRPID)
	if err != nil {
		return err
	}
	if !found || !s.now().Before(ch.ExpiresAt) || rawAuthKeyID == ([8]byte{}) || ch.AuthKeyID != rawAuthKeyID || ch.UserID != userID {
		return domain.ErrSRPIDInvalid
	}
	if !has || !bytes.Equal(stored.Verifier, ch.Verifier) ||
		!bytes.Equal(stored.Salt1, ch.Salt1) || !bytes.Equal(stored.Salt2, ch.Salt2) {
		// challenge 签发之后密码被改过或删掉了。
		return domain.ErrSRPPasswordChanged
	}
	if err := srp.Verify(srp.Challenge{
		Verifier: ch.Verifier,
		Secret:   ch.Secret,
		B:        ch.B,
		Salt1:    ch.Salt1,
		Salt2:    ch.Salt2,
	}, current.A, current.M1); err != nil {
		return mapVerifyError(err)
	}
	return nil
}

// sendChangeNotice 由 777000 给 userID 发一条登录密码改动通知（尽力而为，见 WithChangeNotices）。
func (s *Service) sendChangeNotice(ctx context.Context, userID int64, change passwordChange) {
	if s.notices == nil {
		return
	}
	title, body := changeNoticeText(change)
	text := title + "\n" + body
	// 密码已经改好了：客户端改完立刻断开（例如被盗会话改完就走）也不能让通知丢掉，
	// 所以不跟随请求的取消，只给一个上限。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), changeNoticeTimeout)
	defer cancel()
	randomID, err := randomSRPID()
	if err == nil {
		_, err = s.notices.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:    domain.OfficialSystemUserID,
			RecipientUserID: userID,
			RandomID:        randomID,
			Message:         text,
			Entities:        []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: len(title)}},
			Date:            int(s.now().Unix()),
		})
	}
	if err != nil && s.log != nil {
		s.log.Warn("login password change notice", zap.Int64("user_id", userID), zap.Error(err))
	}
}

// changeNoticeText 返回通知的标题行和正文。文案与 777000 的新设备登录通知一样是英文，
// 标题加粗，末尾提示在 Settings > Devices 里结束可疑会话。
func changeNoticeText(change passwordChange) (title, body string) {
	const unknown = "\n\nIf this wasn't you, terminate unknown sessions in Settings > Devices (or Privacy & Security > Active Sessions) and change your login password."
	switch change {
	case passwordSet:
		return "Login password set.", "A login password was set for your account. You can now sign in with your phone number and this password." + unknown
	case passwordChanged:
		return "Login password changed.", "The login password of your account was changed. Sign in with the new password from now on." + unknown
	case passwordRemoved:
		return "Login password removed.", "The login password of your account was removed. You can only sign in with a login code now." +
			"\n\nIf this wasn't you, terminate unknown sessions in Settings > Devices (or Privacy & Security > Active Sessions)."
	default:
		return "", ""
	}
}
