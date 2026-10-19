package storetest

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// LoginPasswordAccountDeletionFixture 是同一份底层数据上的 UserStore、LoginPasswordStore
// 和 AccountLifecycleStore，以及两个还没有人占用的手机号。
type LoginPasswordAccountDeletionFixture struct {
	Users      store.UserStore
	Passwords  store.LoginPasswordStore
	Lifecycle  store.AccountLifecycleStore
	Phone      string
	OtherPhone string
}

// LoginPasswordAccountDeletionContract 是"注销账号时删除登录密码"的契约：用户是软删除，
// 账号行还在，不能指望外键级联，所以注销本身必须删掉 login password；别人的登录密码不受影响；
// 对已注销的账号再执行一次注销不报错。
func LoginPasswordAccountDeletionContract(t *testing.T, newFixture func(t *testing.T) LoginPasswordAccountDeletionFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("account deletion removes the login password", func(t *testing.T) {
		f := newFixture(t)
		doomed, err := f.Passwords.CreateUserWithLoginPassword(ctx, domain.User{AccessHash: 1, Phone: f.Phone, FirstName: "Doomed"}, testLoginPassword(20))
		if err != nil {
			t.Fatalf("create doomed account: %v", err)
		}
		kept, err := f.Passwords.CreateUserWithLoginPassword(ctx, domain.User{AccessHash: 2, Phone: f.OtherPhone, FirstName: "Kept"}, testLoginPassword(30))
		if err != nil {
			t.Fatalf("create kept account: %v", err)
		}

		result, err := f.Lifecycle.ExecuteAccountDeletion(ctx, doomed.ID, domain.AccountDeletionManual, "contract", time.Now().UTC())
		if err != nil {
			t.Fatalf("execute account deletion: %v", err)
		}
		if !result.Changed || !result.User.Deleted {
			t.Fatalf("deletion result changed=%v deleted=%v, want a fresh tombstone", result.Changed, result.User.Deleted)
		}
		if _, found, err := f.Passwords.GetLoginPassword(ctx, doomed.ID); err != nil || found {
			t.Fatalf("login password of the deleted account found=%v err=%v, want deleted", found, err)
		}
		if _, found, err := f.Users.ByPhone(ctx, f.Phone); err != nil || found {
			t.Fatalf("phone owner after deletion found=%v err=%v, want none", found, err)
		}
		assertLoginPassword(t, f.Passwords, kept.ID, testLoginPassword(30))

		// 已经是墓碑：再注销一次是幂等的空操作。
		again, err := f.Lifecycle.ExecuteAccountDeletion(ctx, doomed.ID, domain.AccountDeletionManual, "contract", time.Now().UTC())
		if err != nil || again.Changed {
			t.Fatalf("second deletion changed=%v err=%v, want an unchanged no-op", again.Changed, err)
		}
	})

	t.Run("account deletion without a login password", func(t *testing.T) {
		f := newFixture(t)
		u, err := f.Users.Create(ctx, domain.User{AccessHash: 3, Phone: f.Phone, FirstName: "Plain"})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		if result, err := f.Lifecycle.ExecuteAccountDeletion(ctx, u.ID, domain.AccountDeletionManual, "", time.Now().UTC()); err != nil || !result.Changed {
			t.Fatalf("execute account deletion changed=%v err=%v, want a tombstone", result.Changed, err)
		}
		if _, found, err := f.Passwords.GetLoginPassword(ctx, u.ID); err != nil || found {
			t.Fatalf("login password found=%v err=%v, want none", found, err)
		}
	})
}
