package storetest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// LoginPasswordFixture 是同一份底层数据上的 UserStore 与 LoginPasswordStore，
// 以及一个还没有人占用的手机号。
type LoginPasswordFixture struct {
	Users     store.UserStore
	Passwords store.LoginPasswordStore
	Phone     string
}

// LoginPasswordContract 是登录密码存储的契约：读取、设置、删除，拒绝不存在的账号和不完整的
// 材料。建号时一起写入登录密码的原子性见 AccountCreationContract。每个子测试调用 newFixture
// 取得自己的号码。
func LoginPasswordContract(t *testing.T, newFixture func(t *testing.T) LoginPasswordFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("set get delete", func(t *testing.T) {
		f := newFixture(t)
		u, err := f.Users.Create(ctx, domain.User{AccessHash: 1, Phone: f.Phone, FirstName: "Owner"})
		if err != nil {
			t.Fatalf("create user: %v", err)
		}
		if _, found, err := f.Passwords.GetLoginPassword(ctx, u.ID); err != nil || found {
			t.Fatalf("get before set found=%v err=%v, want not found", found, err)
		}
		first := testLoginPassword(1)
		if err := f.Passwords.SetLoginPassword(ctx, u.ID, first); err != nil {
			t.Fatalf("set login password: %v", err)
		}
		assertLoginPassword(t, f.Passwords, u.ID, first)
		got, _, _ := f.Passwords.GetLoginPassword(ctx, u.ID)
		if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
			t.Fatalf("timestamps created=%v updated=%v, want both set", got.CreatedAt, got.UpdatedAt)
		}

		second := testLoginPassword(2)
		if err := f.Passwords.SetLoginPassword(ctx, u.ID, second); err != nil {
			t.Fatalf("replace login password: %v", err)
		}
		assertLoginPassword(t, f.Passwords, u.ID, second)
		replaced, _, _ := f.Passwords.GetLoginPassword(ctx, u.ID)
		if replaced.UpdatedAt.Before(got.UpdatedAt) {
			t.Fatalf("updated_at went backwards: %v -> %v", got.UpdatedAt, replaced.UpdatedAt)
		}

		deleted, err := f.Passwords.DeleteLoginPassword(ctx, u.ID)
		if err != nil || !deleted {
			t.Fatalf("delete login password deleted=%v err=%v, want true", deleted, err)
		}
		if _, found, err := f.Passwords.GetLoginPassword(ctx, u.ID); err != nil || found {
			t.Fatalf("get after delete found=%v err=%v, want not found", found, err)
		}
		deleted, err = f.Passwords.DeleteLoginPassword(ctx, u.ID)
		if err != nil || deleted {
			t.Fatalf("second delete deleted=%v err=%v, want false", deleted, err)
		}
	})

	t.Run("set rejects missing user and incomplete material", func(t *testing.T) {
		f := newFixture(t)
		const missingUserID = int64(1) << 60
		if err := f.Passwords.SetLoginPassword(ctx, missingUserID, testLoginPassword(1)); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("set on missing user err=%v, want ErrUserNotFound", err)
		}
		u, err := f.Users.Create(ctx, domain.User{AccessHash: 1, Phone: f.Phone, FirstName: "Owner"})
		if err != nil {
			t.Fatalf("create user: %v", err)
		}
		incomplete := testLoginPassword(1)
		incomplete.Verifier = nil
		if err := f.Passwords.SetLoginPassword(ctx, u.ID, incomplete); !errors.Is(err, store.ErrLoginPasswordInvalid) {
			t.Fatalf("set incomplete err=%v, want ErrLoginPasswordInvalid", err)
		}
		if _, found, err := f.Passwords.GetLoginPassword(ctx, u.ID); err != nil || found {
			t.Fatalf("get after rejected set found=%v err=%v, want not found", found, err)
		}
	})
}

// testLoginPassword 生成一份可区分的登录密码材料（不是真实 SRP 值，存储层不做数学校验）。
func testLoginPassword(seed byte) domain.LoginPassword {
	return domain.LoginPassword{
		Salt1:    bytes.Repeat([]byte{seed}, 40),
		Salt2:    bytes.Repeat([]byte{seed + 1}, 16),
		Verifier: bytes.Repeat([]byte{seed + 2}, 256),
	}
}

func assertLoginPassword(t *testing.T, passwords store.LoginPasswordStore, userID int64, want domain.LoginPassword) {
	t.Helper()
	got, found, err := passwords.GetLoginPassword(context.Background(), userID)
	if err != nil || !found {
		t.Fatalf("get login password found=%v err=%v, want found", found, err)
	}
	if !bytes.Equal(got.Salt1, want.Salt1) || !bytes.Equal(got.Salt2, want.Salt2) || !bytes.Equal(got.Verifier, want.Verifier) {
		t.Fatalf("login password = salt1 %x salt2 %x verifier %x..., want salt1 %x salt2 %x verifier %x...",
			got.Salt1, got.Salt2, verifierPrefix(got.Verifier), want.Salt1, want.Salt2, verifierPrefix(want.Verifier))
	}
}

func verifierPrefix(b []byte) []byte {
	if len(b) > 8 {
		return b[:8]
	}
	return b
}
