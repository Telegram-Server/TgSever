package loginpassword_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/app/auth"
	"telesrv/internal/app/loginpassword"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 注册开关（#76）：注册关闭时，密码登录这条路不能用来新建账号；已有账号不受影响。
// 判断本身是 auth.Service.CheckSignUpAllowed（唯一的判断点），这里经 fakeFlows 注入
// 一个真实的 auth.Service，开关可在运行时切换。

// signUpSwitch 是运行时可切换的注册开关（默认开放）。
type signUpSwitch struct{ disabled atomic.Bool }

func (s *signUpSwitch) allowed(context.Context) (bool, error) { return !s.disabled.Load(), nil }
func (s *signUpSwitch) set(enabled bool)                      { s.disabled.Store(!enabled) }

func newSignUpSwitchFixture(t *testing.T, opts ...loginpassword.Option) (*fixture, *signUpSwitch) {
	t.Helper()
	sw := &signUpSwitch{}
	gate := auth.NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		auth.WithSignUpAllowed(sw.allowed))
	f := &fixture{users: memory.NewUserStore(), flows: &fakeFlows{gate: gate}, now: time.Unix(1_800_000_000, 0)}
	f.passwords = memory.NewLoginPasswordStore(f.users)
	opts = append([]loginpassword.Option{loginpassword.WithEnabled(true), loginpassword.WithClock(f.clock)}, opts...)
	f.svc = loginpassword.NewService(f.users, f.passwords, memory.NewLoginPasswordChallengeStore(f.clock), f.flows, opts...)
	return f, sw
}

func TestSignUpSwitchGetLoginPassword(t *testing.T) {
	const (
		newNumber    = "+15550376001"
		withPassword = "15550376002"
		noPassword   = "15550376003"
	)
	for _, open := range []bool{true, false} {
		f, sw := newSignUpSwitchFixture(t)
		ctx := context.Background()
		f.account(t, domain.User{Phone: withPassword, FirstName: "Has"}, "pw")
		if _, err := f.users.Create(ctx, domain.User{Phone: noPassword, FirstName: "Code"}); err != nil {
			t.Fatalf("create code-only account: %v", err)
		}
		sw.set(open)

		// 新号码：开放时返回注册参数（has_password=false），关闭时 SIGN_UP_DISABLED。
		got, err := f.svc.GetLoginPassword(ctx, testAuthKey, newNumber)
		if open {
			if err != nil || got.HasPassword || len(got.SecureRandom) == 0 {
				t.Fatalf("open: new number = %+v, %v; want sign-up settings", got, err)
			}
		} else if !errors.Is(err, domain.ErrSignUpDisabled) {
			t.Fatalf("closed: new number err = %v, want ErrSignUpDisabled", err)
		}

		// 已有账号的结果与开关无关。
		got, err = f.svc.GetLoginPassword(ctx, testAuthKey, "+"+withPassword)
		if err != nil || !got.HasPassword || got.SRPID == 0 {
			t.Fatalf("open=%v: account with login password = %+v, %v; want has_password=true", open, got, err)
		}
		if _, err := f.svc.GetLoginPassword(ctx, testAuthKey, "+"+noPassword); !errors.Is(err, domain.ErrPasswordLoginNotSet) {
			t.Fatalf("open=%v: account without login password err = %v, want ErrPasswordLoginNotSet", open, err)
		}
	}
}

func TestSignUpSwitchSignUpWithPassword(t *testing.T) {
	const (
		newNumber = "15550376101"
		existing  = "15550376102"
	)
	for _, open := range []bool{true, false} {
		f, sw := newSignUpSwitchFixture(t)
		ctx := context.Background()
		f.account(t, domain.User{Phone: existing, FirstName: "Has"}, "pw")
		sw.set(open)

		u, err := f.svc.SignUp(ctx, domain.Authorization{}, "+"+newNumber, "New", "", newSettings(t, "secret"))
		_, created, lookupErr := f.users.ByPhone(ctx, newNumber)
		if lookupErr != nil {
			t.Fatal(lookupErr)
		}
		if open {
			if err != nil || u.ID == 0 || !created {
				t.Fatalf("open: SignUp = %+v, %v (created=%v); want a new account", u, err, created)
			}
		} else if !errors.Is(err, domain.ErrSignUpDisabled) || created {
			t.Fatalf("closed: SignUp err = %v (created=%v); want ErrSignUpDisabled and no account", err, created)
		}

		// 已被占用的号码：开放时照旧是 PHONE_NUMBER_OCCUPIED；关闭时这条路一律不能注册。
		_, err = f.svc.SignUp(ctx, domain.Authorization{}, "+"+existing, "Again", "", newSettings(t, "secret"))
		want := domain.ErrPhoneNumberOccupied
		if !open {
			want = domain.ErrSignUpDisabled
		}
		if !errors.Is(err, want) {
			t.Fatalf("open=%v: SignUp on an existing number err = %v, want %v", open, err, want)
		}

		// 已有账号的密码登录不受影响。
		settings, err := f.svc.GetLoginPassword(ctx, testAuthKey, "+"+existing)
		if err != nil {
			t.Fatalf("open=%v: GetLoginPassword: %v", open, err)
		}
		if _, err := f.svc.SignIn(ctx, domain.Authorization{}, testAuthKey, "+"+existing, proof(t, settings, "pw")); err != nil {
			t.Fatalf("open=%v: SignIn for an existing account: %v", open, err)
		}
	}
}

// 查询时注册还开着、提交注册前被关闭：注册这一步仍然拦得住。
func TestSignUpSwitchClosedAfterGetLoginPasswordStillBlocksSignUp(t *testing.T) {
	f, sw := newSignUpSwitchFixture(t)
	ctx := context.Background()
	if _, err := f.svc.GetLoginPassword(ctx, testAuthKey, "+15550376201"); err != nil {
		t.Fatalf("GetLoginPassword while open: %v", err)
	}
	sw.set(false)
	if _, err := f.svc.SignUp(ctx, domain.Authorization{}, "+15550376201", "Late", "", newSettings(t, "secret")); !errors.Is(err, domain.ErrSignUpDisabled) {
		t.Fatalf("SignUp after closing err = %v, want ErrSignUpDisabled", err)
	}
	// 重新开放后同一个号码可以注册。
	sw.set(true)
	if _, err := f.svc.SignUp(ctx, domain.Authorization{}, "+15550376201", "Late", "", newSettings(t, "secret")); err != nil {
		t.Fatalf("SignUp after reopening: %v", err)
	}
}

// 检查顺序：密码登录总开关先于注册开关。
func TestSignUpSwitchPasswordLoginSwitchComesFirst(t *testing.T) {
	f, sw := newSignUpSwitchFixture(t, loginpassword.WithEnabled(false))
	sw.set(false)
	ctx := context.Background()
	if _, err := f.svc.GetLoginPassword(ctx, testAuthKey, "+15550376301"); !errors.Is(err, domain.ErrPasswordLoginDisabled) {
		t.Fatalf("GetLoginPassword err = %v, want ErrPasswordLoginDisabled", err)
	}
	if _, err := f.svc.SignUp(ctx, domain.Authorization{}, "+15550376301", "New", "", newSettings(t, "secret")); !errors.Is(err, domain.ErrPasswordLoginDisabled) {
		t.Fatalf("SignUp err = %v, want ErrPasswordLoginDisabled", err)
	}
}
