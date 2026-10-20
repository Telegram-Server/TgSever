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

// 管理后台覆盖值与环境变量默认值的优先级：有覆盖值时以覆盖值为准，没有时跟随
// 环境变量；清除覆盖后立刻恢复跟随环境变量。服务端每次判断都现读 identity 存储。

func signUpOverride(v bool) *bool { return &v }

func newSignUpOverrideService(t *testing.T, envDefault bool) (*Service, *identity.Store) {
	t.Helper()
	store := identity.NewStore(t.TempDir())
	f := newSignUpSwitchFixture(t, WithSignUpAllowed(IdentitySignUpAllowed(store, envDefault)))
	return f.svc, store
}

func assertSignUpAllowed(t *testing.T, svc *Service, want bool, step string) {
	t.Helper()
	got, err := svc.SignUpAllowed(context.Background())
	if err != nil {
		t.Fatalf("%s: SignUpAllowed err=%v", step, err)
	}
	if got != want {
		t.Fatalf("%s: SignUpAllowed=%v, want %v", step, got, want)
	}
}

func TestSignUpOverrideTakesPrecedenceOverEnvAndClearingFollowsEnvAgain(t *testing.T) {
	for _, env := range []bool{true, false} {
		svc, store := newSignUpOverrideService(t, env)

		assertSignUpAllowed(t, svc, env, "no override follows env")

		if err := store.SetSignUpOverride(signUpOverride(!env)); err != nil {
			t.Fatal(err)
		}
		assertSignUpAllowed(t, svc, !env, "override opposite of env")

		if err := store.SetSignUpOverride(signUpOverride(env)); err != nil {
			t.Fatal(err)
		}
		assertSignUpAllowed(t, svc, env, "override equal to env")

		if err := store.SetSignUpOverride(signUpOverride(!env)); err != nil {
			t.Fatal(err)
		}
		if err := store.SetSignUpOverride(nil); err != nil {
			t.Fatal(err)
		}
		assertSignUpAllowed(t, svc, env, "cleared override follows env again")
	}
}

func TestSignUpOverrideOffTakesEffectWithoutRestartAtSendCode(t *testing.T) {
	ctx := context.Background()
	svc, store := newSignUpOverrideService(t, true)

	if _, err := svc.SendCode(ctx, "+15550007701"); err != nil {
		t.Fatalf("SendCode while env opens sign-up: %v", err)
	}
	if err := store.SetSignUpOverride(signUpOverride(false)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendCode(ctx, "+15550007702"); !errors.Is(err, domain.ErrSignUpDisabled) {
		t.Fatalf("SendCode after admin override off err=%v, want ErrSignUpDisabled", err)
	}
	if err := store.SetSignUpOverride(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SendCode(ctx, "+15550007703"); err != nil {
		t.Fatalf("SendCode after clearing override: %v", err)
	}
}

func TestSignUpOverrideUnreadableIdentityFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newSignUpSwitchFixture(t, WithSignUpAllowed(IdentitySignUpAllowed(identity.NewStore(dir), true)))

	if _, err := f.svc.SendCode(context.Background(), "+15550007704"); err == nil {
		t.Fatal("SendCode with unreadable identity store succeeded, want fail closed")
	}
}
