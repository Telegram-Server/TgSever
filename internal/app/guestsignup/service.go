// Package guestsignup 实现游客注册（telesrv.signUpAsGuest，docs/guest-login.md）：客户端不带
// 手机号注册，服务端从 +1 虚构号段分配号码；名字为空时随机生成「形容词的名词」。
//
// 同一 IP 键（IPv4 地址或 IPv6 /64 前缀）在 IP 绑定天数内再次游客注册，得到的是同一个账号
// （直接登录它），不会建新号。绑定的账号一旦"转正"——设置了登录密码或 2FA、换绑了号码、
// 注销——就不再复用：同一出口 IP 后面的其他人（NAT）点游客登录时拿到新号，而不是进入别人
// 已经保护起来的账号。转正在读绑定时现场判断，不需要在设置密码、换号的各条路径上解绑。
//
// 游客登录开启时，游客注册绕过注册开关；验证码登录开关与它无关。
package guestsignup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Flows 是游客注册复用的 auth 服务两段流程，生产中就是 *auth.Service。
type Flows interface {
	// SignUpGuestWithAccountCreator 校验号码与名字后经 create 建号、绑定授权、写欢迎消息，
	// 不检查注册开关。
	SignUpGuestWithAccountCreator(ctx context.Context, a domain.Authorization, phone, firstName, lastName string, create auth.SignUpAccountFunc) (domain.User, error)
	// FinishVerifiedSignIn 是已有账号的登录收尾（复用 IP 绑定的账号时）。
	FinishVerifiedSignIn(ctx context.Context, a domain.Authorization, u domain.User) (domain.User, error)
}

// Deps 是游客注册服务的依赖。
type Deps struct {
	// Settings 现读游客登录开关与 IP 绑定天数（auth.IdentityGuestLogin）。
	Settings auth.GuestLoginFunc
	Users    store.UserStore
	Guests   store.GuestAccountStore
	Accounts store.AccountCreationStore
	// LoginPasswords 与 Passwords 用来判断绑定的账号是否已转正（设置了登录密码或 2FA）。
	LoginPasswords store.LoginPasswordStore
	Passwords      store.PasswordStore
	Flows          Flows
}

// Service 是游客注册业务服务。
type Service struct {
	deps        Deps
	now         func() time.Time
	randomPhone func() (string, error)
	randomName  func() (string, error)
}

// Option 调整游客注册服务。
type Option func(*Service)

// WithClock 注入判断绑定是否过期用的时钟，默认 time.Now。
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithGenerators 替换号码与名字的随机生成（测试用）；nil 表示保留默认。
func WithGenerators(phone, name func() (string, error)) Option {
	return func(s *Service) {
		if phone != nil {
			s.randomPhone = phone
		}
		if name != nil {
			s.randomName = name
		}
	}
}

// 号码被占用时换一个号码重试的次数；号码池约 4.5 万个，这么多次都撞上说明号码池快用完了。
const phoneAttempts = 32

// 建号时 IP 绑定被并发请求换掉（比较并交换失败）后重读绑定的次数。
const bindingAttempts = 3

func NewService(deps Deps, opts ...Option) *Service {
	s := &Service{deps: deps, now: time.Now, randomPhone: RandomNumber, randomName: RandomName}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Settings 返回游客登录的实际设置；读失败时返回错误（调用方按关闭处理）。
func (s *Service) Settings(ctx context.Context) (domain.GuestLoginSettings, error) {
	if s == nil || s.deps.Settings == nil {
		return domain.GuestLoginSettings{}, nil
	}
	return s.deps.Settings(ctx)
}

// SignUp 处理 telesrv.signUpAsGuest：游客登录关闭时返回 domain.ErrGuestLoginDisabled；拿不到
// 客户端 IP 时返回 domain.ErrGuestIPUnavailable。IP 键在绑定天数内绑着一个未转正的账号时直接
// 登录它（返回值与 FinishVerifiedSignIn 相同）；否则建一个新的游客账号并把 IP 键绑定到它。
// 名字只在新建账号时使用：first_name 为空时随机生成。
func (s *Service) SignUp(ctx context.Context, a domain.Authorization, clientIP, firstName, lastName string) (domain.User, error) {
	settings, err := s.Settings(ctx)
	if err != nil {
		return domain.User{}, fmt.Errorf("resolve guest login switch: %w", err)
	}
	if !settings.Enabled {
		return domain.User{}, domain.ErrGuestLoginDisabled
	}
	if s.deps.Users == nil || s.deps.Guests == nil || s.deps.Accounts == nil || s.deps.Flows == nil {
		return domain.User{}, errors.New("guestsignup: service is not configured")
	}
	ipKey, ok := IPKey(clientIP)
	if !ok {
		return domain.User{}, domain.ErrGuestIPUnavailable
	}
	for attempt := 0; attempt < bindingAttempts; attempt++ {
		binding, found, err := s.deps.Guests.GuestIPBinding(ctx, ipKey)
		if err != nil {
			return domain.User{}, err
		}
		var previous *int64
		if found {
			u, reusable, err := s.reusableAccount(ctx, binding, settings)
			if err != nil {
				return domain.User{}, err
			}
			if reusable {
				return s.deps.Flows.FinishVerifiedSignIn(ctx, a, u)
			}
			previous = &binding.UserID
		}
		u, err := s.create(ctx, a, ipKey, previous, firstName, lastName)
		if errors.Is(err, domain.ErrGuestIPBindingChanged) {
			// 同一 IP 的另一个请求刚换了绑定：重读，多半会直接登录它建的那个账号。
			continue
		}
		return u, err
	}
	return domain.User{}, fmt.Errorf("guestsignup: ip binding kept changing: %w", domain.ErrGuestIPBindingChanged)
}

// reusableAccount 判断 IP 键绑定的账号能否直接复用：绑定没过期、账号还在、仍是注册时分配的
// 号码、没有设置登录密码也没有开 2FA。任何一项读失败都返回错误（不在不确定时放人进账号）。
func (s *Service) reusableAccount(ctx context.Context, binding domain.GuestIPBinding, settings domain.GuestLoginSettings) (domain.User, bool, error) {
	if !s.now().Before(binding.BoundAt.Add(settings.IPBindWindow())) {
		return domain.User{}, false, nil
	}
	u, found, err := s.deps.Users.ByID(ctx, binding.UserID)
	if err != nil {
		return domain.User{}, false, err
	}
	if !found || u.Deleted || u.Bot {
		return domain.User{}, false, nil
	}
	guest, found, err := s.deps.Guests.GuestAccount(ctx, u.ID)
	if err != nil {
		return domain.User{}, false, err
	}
	if !found || guest.Phone != u.Phone {
		return domain.User{}, false, nil
	}
	if s.deps.LoginPasswords != nil {
		_, hasLoginPassword, err := s.deps.LoginPasswords.GetLoginPassword(ctx, u.ID)
		if err != nil {
			return domain.User{}, false, err
		}
		if hasLoginPassword {
			return domain.User{}, false, nil
		}
	}
	if s.deps.Passwords != nil {
		twoFA, found, err := s.deps.Passwords.GetByUser(ctx, u.ID)
		if err != nil {
			return domain.User{}, false, err
		}
		if found && twoFA.HasPassword {
			return domain.User{}, false, nil
		}
	}
	return u, true, nil
}

// create 建一个新的游客账号：号码被占用时换号重试，名字为空时随机生成。
func (s *Service) create(ctx context.Context, a domain.Authorization, ipKey string, previous *int64, firstName, lastName string) (domain.User, error) {
	if strings.TrimSpace(firstName) == "" {
		name, err := s.randomName()
		if err != nil {
			return domain.User{}, err
		}
		firstName = name
	}
	guest := &store.GuestAccountExtras{IPKey: ipKey, PreviousUserID: previous}
	createAccount := func(ctx context.Context, u domain.User, permissions *domain.AccountPermissions) (domain.User, error) {
		return s.deps.Accounts.CreateAccount(ctx, u, store.AccountExtras{Permissions: permissions, Guest: guest})
	}
	for attempt := 0; attempt < phoneAttempts; attempt++ {
		phone, err := s.randomPhone()
		if err != nil {
			return domain.User{}, err
		}
		u, err := s.deps.Flows.SignUpGuestWithAccountCreator(ctx, a, phone, firstName, lastName, createAccount)
		if errors.Is(err, domain.ErrPhoneNumberOccupied) {
			continue
		}
		return u, err
	}
	return domain.User{}, domain.ErrGuestNumberPoolExhausted
}
