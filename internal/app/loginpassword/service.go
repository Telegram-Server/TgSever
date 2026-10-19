// Package loginpassword 实现手机号 + 密码登录（telesrv 扩展协议，#27）的业务逻辑：
// 查询注册/登录参数、密码注册、密码登录，以及后续票的修改。
//
// 登录密码与 2FA 云密码互相独立，各存各的；SRP 数学与 2FA 共用 internal/srp。
// 整个功能由总开关控制，关闭时每个方法都返回 domain.ErrPasswordLoginDisabled。
package loginpassword

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"telesrv/internal/app/account"
	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/srp"
	"telesrv/internal/store"
)

// challengeTTL 是密码登录 SRP challenge 的有效期。
const challengeTTL = 5 * time.Minute

// errNoAuthKey 表示请求的 context 里没有原始 auth_key，challenge 无从绑定。
// 经过 MTProto 握手的请求一定带有它，出现即是内部错误。
var errNoAuthKey = errors.New("loginpassword: request has no raw auth key")

// SignUpCompleter 是注册的后半段（auth.Service.SignUpWithAccountCreator）：按 auth.signUp 的
// 规则校验号码和名字，用 create 建号，然后赠送会员、绑定授权、写欢迎消息。
type SignUpCompleter interface {
	SignUpWithAccountCreator(ctx context.Context, a domain.Authorization, phone, firstName, lastName string, create auth.CreateAccountFunc) (domain.User, error)
}

// SignInCompleter 是登录的收尾（auth.Service.FinishVerifiedSignIn）：与验证码登录共用
// finishSignIn，处理系统账号、已注销账号、2FA password_pending、绑定授权和欢迎消息。
type SignInCompleter interface {
	FinishVerifiedSignIn(ctx context.Context, a domain.Authorization, u domain.User) (domain.User, error)
}

// AuthFlows 是登录密码复用的 auth 服务两段流程，生产中就是 *auth.Service。
type AuthFlows interface {
	SignUpCompleter
	SignInCompleter
}

// Service 是登录密码业务服务。
type Service struct {
	enabled    bool
	users      store.UserStore
	passwords  store.LoginPasswordStore
	challenges store.LoginPasswordChallengeStore
	flows      AuthFlows
	now        func() time.Time
}

// Option 调整登录密码服务。
type Option func(*Service)

// WithEnabled 设置总开关（TELESRV_PASSWORD_LOGIN_ENABLED），默认关闭。
func WithEnabled(enabled bool) Option {
	return func(s *Service) {
		s.enabled = enabled
	}
}

// WithClock 注入时钟（challenge 的发出时刻与过期判断），默认 time.Now。
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// NewService 创建登录密码服务。users 用来按号码查账号，passwords 保存登录密码，
// challenges 暂存密码登录的 SRP challenge，flows 提供与验证码登录/注册共用的收尾。
func NewService(users store.UserStore, passwords store.LoginPasswordStore, challenges store.LoginPasswordChallengeStore, flows AuthFlows, opts ...Option) *Service {
	s := &Service{users: users, passwords: passwords, challenges: challenges, flows: flows, now: time.Now}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Enabled 报告总开关是否打开，并且依赖齐全。
func (s *Service) Enabled() bool {
	return s != nil && s.enabled && s.users != nil && s.passwords != nil && s.challenges != nil && s.flows != nil
}

// GetLoginPassword 处理 telesrv.getLoginPassword，有三种结果：
//   - 号码没有账号：返回注册用的参数（has_password=false、new_algo、new_secure_algo、
//     secure_random），客户端据此进入注册；
//   - 有账号但没设登录密码：返回 domain.ErrPasswordLoginNotSet；
//   - 有账号且设了登录密码：生成一个新的 SRP challenge 存进 challenge store（只有
//     rawAuthKeyID 能用，challengeTTL 后过期），返回 has_password=true、current_algo
//     （salt1/salt2 取存储的登录密码）、srp_B、srp_id，以及同样的 new_algo 等参数。
//
// challenge 不写进用户记录：每次调用都只是多一个独立的 challenge，挤不掉别人手里的那个。
func (s *Service) GetLoginPassword(ctx context.Context, rawAuthKeyID [8]byte, phone string) (domain.PasswordSettings, error) {
	if !s.Enabled() {
		return domain.PasswordSettings{}, domain.ErrPasswordLoginDisabled
	}
	phone, err := auth.NormalizeSignUpPhone(phone)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	u, found, err := s.users.ByPhone(ctx, phone)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	if !found {
		return account.NoPasswordSettings(), nil
	}
	password, found, err := s.passwords.GetLoginPassword(ctx, u.ID)
	if err != nil {
		return domain.PasswordSettings{}, err
	}
	if !found {
		return domain.PasswordSettings{}, domain.ErrPasswordLoginNotSet
	}
	if rawAuthKeyID == ([8]byte{}) {
		return domain.PasswordSettings{}, errNoAuthKey
	}
	ch, err := s.issueChallenge(ctx, u.ID, phone, rawAuthKeyID, password)
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

// issueChallenge 为登录密码生成 b、B 和随机 srp_id 并存进 challenge store。
// srp_id 撞上未取走的 challenge 时换一个重试，绝不覆盖。
func (s *Service) issueChallenge(ctx context.Context, userID int64, phone string, rawAuthKeyID [8]byte, password domain.LoginPassword) (store.LoginPasswordChallenge, error) {
	secret, b, err := srp.NewChallenge(password.Verifier)
	if err != nil {
		// 存储里的 verifier 越界只可能是数据损坏，按内部错误处理。
		return store.LoginPasswordChallenge{}, fmt.Errorf("login password challenge for user %d: %w", userID, err)
	}
	ch := store.LoginPasswordChallenge{
		UserID:    userID,
		Phone:     phone,
		AuthKeyID: rawAuthKeyID,
		Secret:    secret,
		B:         b,
		Verifier:  password.Verifier,
		Salt1:     password.Salt1,
		Salt2:     password.Salt2,
		ExpiresAt: s.now().Add(challengeTTL),
	}
	for attempt := 0; ; attempt++ {
		ch.SRPID, err = randomSRPID()
		if err != nil {
			return store.LoginPasswordChallenge{}, err
		}
		err = s.challenges.SaveLoginPasswordChallenge(ctx, ch)
		if !errors.Is(err, store.ErrLoginPasswordChallengeExists) || attempt >= 2 {
			return ch, err
		}
	}
}

// SignIn 处理 telesrv.signInWithPassword：
//  1. 按 srp_id 原子地取出并删除 challenge——不论之后成败，它都已作废，客户端要重新 get；
//  2. challenge 不存在、已过期、不是发给 rawAuthKeyID 的、或手机号对不上：domain.ErrSRPIDInvalid；
//  3. 重新读存储里的登录密码，与 challenge 记录的不一致（期间被改或删）：domain.ErrSRPPasswordChanged；
//  4. SRP 证明不成立：domain.ErrPasswordHashInvalid；
//  5. 通过后交给 auth 的登录收尾（FinishVerifiedSignIn）。账号已注销、系统账号、bot 由那里统一
//     拒绝（auth.ErrSystemUserLoginForbidden，与验证码登录同一条规则），这里不重复判断，
//     所以账号状态只在证明成立之后才会体现在错误码上；开了 2FA 的账号返回用户和
//     domain.ErrSessionPasswordNeeded。
func (s *Service) SignIn(ctx context.Context, a domain.Authorization, rawAuthKeyID [8]byte, phone string, check domain.PasswordCheck) (domain.User, error) {
	if !s.Enabled() {
		return domain.User{}, domain.ErrPasswordLoginDisabled
	}
	if check.Empty {
		return domain.User{}, domain.ErrPasswordHashInvalid
	}
	if check.SRPID == 0 {
		return domain.User{}, domain.ErrSRPIDInvalid
	}
	ch, found, err := s.challenges.TakeLoginPasswordChallenge(ctx, check.SRPID)
	if err != nil {
		return domain.User{}, err
	}
	if !found || !s.now().Before(ch.ExpiresAt) || rawAuthKeyID == ([8]byte{}) || ch.AuthKeyID != rawAuthKeyID {
		return domain.User{}, domain.ErrSRPIDInvalid
	}
	if phone, err = auth.NormalizeSignUpPhone(phone); err != nil || phone != ch.Phone {
		return domain.User{}, domain.ErrSRPIDInvalid
	}
	u, found, err := s.users.ByID(ctx, ch.UserID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, domain.ErrSRPIDInvalid
	}
	if u.Phone != ch.Phone {
		// 号码在发出 challenge 之后换了主人或被改掉。
		return domain.User{}, domain.ErrSRPIDInvalid
	}
	current, found, err := s.passwords.GetLoginPassword(ctx, u.ID)
	if err != nil {
		return domain.User{}, err
	}
	if !found || !bytes.Equal(current.Verifier, ch.Verifier) ||
		!bytes.Equal(current.Salt1, ch.Salt1) || !bytes.Equal(current.Salt2, ch.Salt2) {
		return domain.User{}, domain.ErrSRPPasswordChanged
	}
	if err := srp.Verify(srp.Challenge{
		Verifier: ch.Verifier,
		Secret:   ch.Secret,
		B:        ch.B,
		Salt1:    ch.Salt1,
		Salt2:    ch.Salt2,
	}, check.A, check.M1); err != nil {
		return domain.User{}, mapVerifyError(err)
	}
	return s.flows.FinishVerifiedSignIn(ctx, a, u)
}

// mapVerifyError 把 srp.Verify 的错误映射成登录密码的 domain 错误。challenge 是本服务
// 刚生成的，存档不完整只可能是内部错误；按 SRP_ID_INVALID 让客户端重新获取。
func mapVerifyError(err error) error {
	switch {
	case errors.Is(err, srp.ErrInvalidProof), errors.Is(err, srp.ErrInvalidVerifier):
		return domain.ErrPasswordHashInvalid
	case errors.Is(err, srp.ErrIncompleteChallenge):
		return domain.ErrSRPIDInvalid
	default:
		return err
	}
}

// randomSRPID 返回一个非零的随机 srp_id。
func randomSRPID() (int64, error) {
	var buf [8]byte
	for {
		if _, err := rand.Read(buf[:]); err != nil {
			return 0, err
		}
		if id := int64(binary.LittleEndian.Uint64(buf[:])); id != 0 {
			return id, nil
		}
	}
}

// SignUp 处理 telesrv.signUpWithPassword：不经验证码，用手机号和登录密码直接注册，
// 成功后当前授权立即处于登录状态。校验顺序：开关 → 号码 → 名字 → new_settings。
// 建号和写入登录密码在同一个事务里完成；号码已被占用返回 domain.ErrPhoneNumberOccupied。
func (s *Service) SignUp(ctx context.Context, a domain.Authorization, phone, firstName, lastName string, settings domain.PasswordInputSettings) (domain.User, error) {
	if !s.Enabled() {
		return domain.User{}, domain.ErrPasswordLoginDisabled
	}
	phone, err := auth.NormalizeSignUpPhone(phone)
	if err != nil {
		return domain.User{}, err
	}
	firstName, lastName, err = auth.NormalizeSignUpName(firstName, lastName)
	if err != nil {
		return domain.User{}, err
	}
	password, err := newLoginPassword(settings)
	if err != nil {
		return domain.User{}, err
	}
	return s.flows.SignUpWithAccountCreator(ctx, a, phone, firstName, lastName, func(ctx context.Context, u domain.User) (domain.User, error) {
		return s.passwords.CreateUserWithLoginPassword(ctx, u, password)
	})
}

// newLoginPassword 校验客户端提交的 new_settings 并转换成要保存的登录密码。
// 只读 new_algo 和 new_password_hash：带了 email 一律拒绝（登录密码没有找回邮箱），
// hint 忽略。盐和算法的规则与 2FA 设置密码时完全一样（internal/srp.ValidateNewPassword）。
func newLoginPassword(in domain.PasswordInputSettings) (domain.LoginPassword, error) {
	if in.HasEmail {
		return domain.LoginPassword{}, domain.ErrNewSettingsInvalid
	}
	var algo *srp.Algo
	if in.NewAlgo != nil {
		algo = &srp.Algo{Salt1: in.NewAlgo.Salt1, Salt2: in.NewAlgo.Salt2, G: in.NewAlgo.G, P: in.NewAlgo.P}
	}
	if err := srp.ValidateNewPassword(algo, in.NewPasswordHash); err != nil {
		return domain.LoginPassword{}, mapNewPasswordError(err)
	}
	verifier, err := srp.Pad(in.NewPasswordHash)
	if err != nil {
		return domain.LoginPassword{}, domain.ErrPasswordHashInvalid
	}
	return domain.LoginPassword{
		Salt1:    append([]byte(nil), algo.Salt1...),
		Salt2:    append([]byte(nil), algo.Salt2...),
		Verifier: verifier,
	}, nil
}

// mapNewPasswordError 把 srp 包校验新密码的错误映射成与 2FA 一致的 domain 错误。
func mapNewPasswordError(err error) error {
	switch {
	case errors.Is(err, srp.ErrNewSettingsMissing):
		return domain.ErrNewSettingsInvalid
	case errors.Is(err, srp.ErrNewAlgoInvalid):
		return domain.ErrNewSaltInvalid
	case errors.Is(err, srp.ErrInvalidVerifier):
		return domain.ErrPasswordHashInvalid
	default:
		return err
	}
}
