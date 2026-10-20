package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// 验证码登录开关的管理后台覆盖值与环境变量默认值的优先级，与注册开关同构：有覆盖值时
// 以覆盖值为准，没有时跟随环境变量；清除后立即恢复跟随环境变量；每次判断都现读。

func newCodeLoginOverrideService(t *testing.T, envDefault bool) (*Service, *identity.Store) {
	t.Helper()
	store := identity.NewStore(t.TempDir())
	f := newSignUpSwitchFixture(t, WithCodeLoginAllowed(IdentityCodeLoginAllowed(store, envDefault)))
	return f.svc, store
}

func assertCodeLoginAllowed(t *testing.T, svc *Service, want bool, step string) {
	t.Helper()
	got, err := svc.CodeLoginAllowed(context.Background())
	if err != nil {
		t.Fatalf("%s: CodeLoginAllowed err=%v", step, err)
	}
	if got != want {
		t.Fatalf("%s: CodeLoginAllowed=%v, want %v", step, got, want)
	}
}

func TestCodeLoginOverrideTakesPrecedenceOverEnvAndClearingFollowsEnvAgain(t *testing.T) {
	for _, env := range []bool{true, false} {
		svc, store := newCodeLoginOverrideService(t, env)

		assertCodeLoginAllowed(t, svc, env, "no override follows env")

		if err := store.SetCodeLoginOverride(signUpOverride(!env)); err != nil {
			t.Fatal(err)
		}
		assertCodeLoginAllowed(t, svc, !env, "override opposite of env")

		if err := store.SetCodeLoginOverride(signUpOverride(env)); err != nil {
			t.Fatal(err)
		}
		assertCodeLoginAllowed(t, svc, env, "override equal to env")

		if err := store.SetCodeLoginOverride(signUpOverride(!env)); err != nil {
			t.Fatal(err)
		}
		if err := store.SetCodeLoginOverride(nil); err != nil {
			t.Fatal(err)
		}
		assertCodeLoginAllowed(t, svc, env, "cleared override follows env again")
	}
}

func TestCodeLoginOverrideOffTakesEffectWithoutRestartAtSendCode(t *testing.T) {
	ctx := context.Background()
	svc, store := newCodeLoginOverrideService(t, true)

	if _, err := svc.SendCode(ctx, "+15550008801"); err != nil {
		t.Fatalf("SendCode while env opens code login: %v", err)
	}
	if err := store.SetCodeLoginOverride(signUpOverride(false)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendCode(ctx, "+15550008802"); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("SendCode after admin override off err=%v, want ErrCodeLoginDisabled", err)
	}
	if err := store.SetCodeLoginOverride(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendCode(ctx, "+15550008803"); err != nil {
		t.Fatalf("SendCode after clearing override: %v", err)
	}
}

func TestCodeLoginOverrideOnReopensCodeLoginClosedByEnv(t *testing.T) {
	ctx := context.Background()
	svc, store := newCodeLoginOverrideService(t, false)

	if _, err := svc.SendCode(ctx, "+15550008804"); !errors.Is(err, domain.ErrCodeLoginDisabled) {
		t.Fatalf("SendCode while env closes code login err=%v, want ErrCodeLoginDisabled", err)
	}
	if err := store.SetCodeLoginOverride(signUpOverride(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendCode(ctx, "+15550008805"); err != nil {
		t.Fatalf("SendCode after admin override on: %v", err)
	}
}

func TestCodeLoginOverrideUnreadableIdentityFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newSignUpSwitchFixture(t, WithCodeLoginAllowed(IdentityCodeLoginAllowed(identity.NewStore(dir), true)))

	if _, err := f.svc.SendCode(context.Background(), "+15550008806"); err == nil {
		t.Fatal("SendCode with unreadable identity store succeeded, want fail closed")
	}
}

func TestResolveCodeLoginEnabledPrefersOverride(t *testing.T) {
	cases := []struct {
		override *bool
		env      bool
		want     bool
	}{
		{nil, true, true},
		{nil, false, false},
		{signUpOverride(false), true, false},
		{signUpOverride(true), false, true},
	}
	for _, c := range cases {
		if got := domain.ResolveCodeLoginEnabled(c.override, c.env); got != c.want {
			t.Fatalf("ResolveCodeLoginEnabled(%v, %v) = %v, want %v", c.override, c.env, got, c.want)
		}
	}
}
