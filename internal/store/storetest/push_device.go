package storetest

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// PushDeviceAuthorizations 是契约用来删除授权的那部分授权存储：store.AuthorizationStore
// 加上终止会话 / 注销账号走的 Revoke* 方法（memory 与 postgres 都实现）。
type PushDeviceAuthorizations interface {
	store.AuthorizationStore
	RevokeByHash(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error)
	RevokeByUserExcept(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
}

// PushDeviceFixture 是同一份底层数据上的设备登记存储与授权存储：用户 A 有两个已绑定的
// 授权 AuthA1、AuthA2（hash 分别是 HashA1、HashA2），用户 B 有一个授权 AuthB。
type PushDeviceFixture struct {
	Devices        store.PushDeviceStore
	Auths          PushDeviceAuthorizations
	UserA, UserB   int64
	AuthA1, AuthA2 [8]byte
	HashA1, HashA2 int64
	AuthB          [8]byte
	// DeleteAuthKey 删除协议层 auth key（连同它的授权）。
	DeleteAuthKey func(ctx context.Context, authKeyID [8]byte) error
	// DeleteAccount 注销账号（撤销它的全部授权）。
	DeleteAccount func(ctx context.Context, userID int64) error
}

// PushDeviceContract 是设备推送登记存储的契约：登记与覆盖、多授权共用同一个 token、
// 按授权 + 类型删除、授权被删除（各条路径）后登记随之消失、按 user 查询。
// 每个子测试调用 newFixture 取得自己的用户与授权。
func PushDeviceContract(t *testing.T, newFixture func(t *testing.T) PushDeviceFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("register then list by user with every field", func(t *testing.T) {
		f := newFixture(t)
		if got := listPushDevices(t, f, f.UserA); len(got) != 0 {
			t.Fatalf("fresh user devices = %+v, want none", got)
		}
		before := time.Now().Add(-time.Minute)
		want := testPushDevice(f.AuthA1, f.UserA, domain.PushTokenAPNs, "aa01")
		registerPushDevice(t, f, want)
		got := listPushDevices(t, f, f.UserA)
		if len(got) != 1 {
			t.Fatalf("devices = %+v, want one", got)
		}
		assertPushDevice(t, got[0], want)
		if got[0].CreatedAt.Before(before) || got[0].CreatedAt.After(time.Now().Add(time.Minute)) {
			t.Fatalf("created_at = %v, want about now", got[0].CreatedAt)
		}
		if other := listPushDevices(t, f, f.UserB); len(other) != 0 {
			t.Fatalf("user B devices = %+v, want none", other)
		}
	})

	t.Run("empty optional fields round-trip", func(t *testing.T) {
		f := newFixture(t)
		want := domain.PushDevice{AuthKeyID: f.AuthA1, UserID: f.UserA, TokenType: domain.PushTokenFCM, Token: "fcm-token"}
		registerPushDevice(t, f, want)
		got := listPushDevices(t, f, f.UserA)
		if len(got) != 1 {
			t.Fatalf("devices = %+v, want one", got)
		}
		assertPushDevice(t, got[0], want)
	})

	t.Run("re-register overwrites", func(t *testing.T) {
		f := newFixture(t)
		registerPushDevice(t, f, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenAPNs, "aa01"))
		replaced := domain.PushDevice{
			AuthKeyID: f.AuthA1, UserID: f.UserA, TokenType: domain.PushTokenAPNs, Token: "aa02",
			Secret: []byte{9, 9}, LangCode: "ru",
		}
		registerPushDevice(t, f, replaced)
		got := listPushDevices(t, f, f.UserA)
		if len(got) != 1 {
			t.Fatalf("devices = %+v, want the single overwritten one", got)
		}
		assertPushDevice(t, got[0], replaced)
	})

	t.Run("token types are independent per authorization", func(t *testing.T) {
		f := newFixture(t)
		registerPushDevice(t, f, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenAPNs, "aa01"))
		registerPushDevice(t, f, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenVoIP, "bb01"))
		got := listPushDevices(t, f, f.UserA)
		if len(got) != 2 || got[0].TokenType != domain.PushTokenAPNs || got[1].TokenType != domain.PushTokenVoIP {
			t.Fatalf("devices = %+v, want APNs and VoIP registrations", got)
		}
	})

	t.Run("same token under two authorizations is two registrations", func(t *testing.T) {
		f := newFixture(t)
		registerPushDevice(t, f, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenAPNs, "shared"))
		registerPushDevice(t, f, testPushDevice(f.AuthB, f.UserB, domain.PushTokenAPNs, "shared"))
		a := listPushDevices(t, f, f.UserA)
		b := listPushDevices(t, f, f.UserB)
		if len(a) != 1 || a[0].AuthKeyID != f.AuthA1 || len(b) != 1 || b[0].AuthKeyID != f.AuthB {
			t.Fatalf("user A devices = %+v, user B devices = %+v, want one each", a, b)
		}
		if deleted, err := f.Devices.DeletePushDevice(ctx, f.AuthA1, domain.PushTokenAPNs); err != nil || !deleted {
			t.Fatalf("delete A1 deleted=%v err=%v", deleted, err)
		}
		if b := listPushDevices(t, f, f.UserB); len(b) != 1 {
			t.Fatalf("user B devices after deleting A's = %+v, want untouched", b)
		}
	})

	t.Run("delete removes only that authorization and token type", func(t *testing.T) {
		f := newFixture(t)
		registerPushDevice(t, f, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenAPNs, "aa01"))
		registerPushDevice(t, f, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenVoIP, "bb01"))
		registerPushDevice(t, f, testPushDevice(f.AuthA2, f.UserA, domain.PushTokenAPNs, "aa02"))
		deleted, err := f.Devices.DeletePushDevice(ctx, f.AuthA1, domain.PushTokenAPNs)
		if err != nil || !deleted {
			t.Fatalf("delete deleted=%v err=%v, want deleted", deleted, err)
		}
		got := listPushDevices(t, f, f.UserA)
		if len(got) != 2 || got[0].AuthKeyID != f.AuthA1 || got[0].TokenType != domain.PushTokenVoIP ||
			got[1].AuthKeyID != f.AuthA2 || got[1].TokenType != domain.PushTokenAPNs {
			t.Fatalf("devices after delete = %+v, want A1/VoIP and A2/APNs", got)
		}
		if deleted, err := f.Devices.DeletePushDevice(ctx, f.AuthA1, domain.PushTokenAPNs); err != nil || deleted {
			t.Fatalf("second delete deleted=%v err=%v, want nothing deleted", deleted, err)
		}
	})

	t.Run("register for a missing authorization fails", func(t *testing.T) {
		f := newFixture(t)
		missing := [8]byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04}
		err := f.Devices.RegisterPushDevice(ctx, testPushDevice(missing, f.UserA, domain.PushTokenAPNs, "aa01"))
		if !errors.Is(err, store.ErrPushDeviceAuthorizationNotFound) {
			t.Fatalf("register err = %v, want ErrPushDeviceAuthorizationNotFound", err)
		}
		if got := listPushDevices(t, f, f.UserA); len(got) != 0 {
			t.Fatalf("devices = %+v, want none", got)
		}
	})

	removals := []struct {
		name   string
		remove func(t *testing.T, f PushDeviceFixture)
	}{
		{"log out", func(t *testing.T, f PushDeviceFixture) {
			if err := f.Auths.Delete(ctx, f.AuthA1); err != nil {
				t.Fatal(err)
			}
		}},
		{"terminate session by hash", func(t *testing.T, f PushDeviceFixture) {
			if _, found, err := f.Auths.DeleteByHash(ctx, f.UserA, f.HashA1); err != nil || !found {
				t.Fatalf("DeleteByHash found=%v err=%v", found, err)
			}
		}},
		{"revoke session by hash", func(t *testing.T, f PushDeviceFixture) {
			if _, found, err := f.Auths.RevokeByHash(ctx, f.UserA, f.HashA1); err != nil || !found {
				t.Fatalf("RevokeByHash found=%v err=%v", found, err)
			}
		}},
		{"reset other sessions", func(t *testing.T, f PushDeviceFixture) {
			if _, err := f.Auths.DeleteByUserExcept(ctx, f.UserA, f.AuthA2); err != nil {
				t.Fatal(err)
			}
		}},
		{"revoke other sessions", func(t *testing.T, f PushDeviceFixture) {
			if _, err := f.Auths.RevokeByUserExcept(ctx, f.UserA, f.AuthA2); err != nil {
				t.Fatal(err)
			}
		}},
		{"delete auth key", func(t *testing.T, f PushDeviceFixture) {
			if err := f.DeleteAuthKey(ctx, f.AuthA1); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range removals {
		t.Run("removing the authorization drops its registrations: "+tc.name, func(t *testing.T) {
			f := newFixture(t)
			registerPushDevice(t, f, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenAPNs, "aa01"))
			registerPushDevice(t, f, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenVoIP, "bb01"))
			registerPushDevice(t, f, testPushDevice(f.AuthA2, f.UserA, domain.PushTokenAPNs, "aa02"))
			registerPushDevice(t, f, testPushDevice(f.AuthB, f.UserB, domain.PushTokenAPNs, "cc01"))
			tc.remove(t, f)
			got := listPushDevices(t, f, f.UserA)
			if len(got) != 1 || got[0].AuthKeyID != f.AuthA2 {
				t.Fatalf("user A devices = %+v, want only A2's", got)
			}
			if b := listPushDevices(t, f, f.UserB); len(b) != 1 {
				t.Fatalf("user B devices = %+v, want untouched", b)
			}
			// 授权被删除后不能再为它登记。
			err := f.Devices.RegisterPushDevice(ctx, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenAPNs, "aa01"))
			if !errors.Is(err, store.ErrPushDeviceAuthorizationNotFound) {
				t.Fatalf("register on removed authorization err = %v, want ErrPushDeviceAuthorizationNotFound", err)
			}
		})
	}

	t.Run("deleting the account drops every registration of it", func(t *testing.T) {
		f := newFixture(t)
		registerPushDevice(t, f, testPushDevice(f.AuthA1, f.UserA, domain.PushTokenAPNs, "aa01"))
		registerPushDevice(t, f, testPushDevice(f.AuthA2, f.UserA, domain.PushTokenVoIP, "bb02"))
		registerPushDevice(t, f, testPushDevice(f.AuthB, f.UserB, domain.PushTokenAPNs, "cc01"))
		if err := f.DeleteAccount(ctx, f.UserA); err != nil {
			t.Fatalf("delete account: %v", err)
		}
		if got := listPushDevices(t, f, f.UserA); len(got) != 0 {
			t.Fatalf("deleted account devices = %+v, want none", got)
		}
		if b := listPushDevices(t, f, f.UserB); len(b) != 1 {
			t.Fatalf("user B devices = %+v, want untouched", b)
		}
	})
}

func testPushDevice(authKeyID [8]byte, userID int64, tokenType int, token string) domain.PushDevice {
	return domain.PushDevice{
		AuthKeyID:  authKeyID,
		UserID:     userID,
		TokenType:  tokenType,
		Token:      token,
		AppSandbox: true,
		Secret:     bytes.Repeat([]byte{0x5a}, 256),
		NoMuted:    true,
		OtherUIDs:  []int64{userID + 1000, userID + 2000},
		LangCode:   "zh-hans",
	}
}

func registerPushDevice(t *testing.T, f PushDeviceFixture, device domain.PushDevice) {
	t.Helper()
	if err := f.Devices.RegisterPushDevice(context.Background(), device); err != nil {
		t.Fatalf("register %+v: %v", device, err)
	}
}

// listPushDevices 按 (授权, token 类型) 排序后返回，便于断言。
func listPushDevices(t *testing.T, f PushDeviceFixture, userID int64) []domain.PushDevice {
	t.Helper()
	got, err := f.Devices.ListPushDevicesByUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("list devices of user %d: %v", userID, err)
	}
	authOrder := map[[8]byte]int{f.AuthA1: 0, f.AuthA2: 1, f.AuthB: 2}
	slices.SortFunc(got, func(a, b domain.PushDevice) int {
		if a.AuthKeyID != b.AuthKeyID {
			return authOrder[a.AuthKeyID] - authOrder[b.AuthKeyID]
		}
		return a.TokenType - b.TokenType
	})
	return got
}

func assertPushDevice(t *testing.T, got, want domain.PushDevice) {
	t.Helper()
	if got.AuthKeyID != want.AuthKeyID || got.UserID != want.UserID || got.TokenType != want.TokenType ||
		got.Token != want.Token || got.AppSandbox != want.AppSandbox || !bytes.Equal(got.Secret, want.Secret) ||
		got.NoMuted != want.NoMuted || !slices.Equal(got.OtherUIDs, want.OtherUIDs) || got.LangCode != want.LangCode {
		t.Fatalf("device = %+v, want %+v", got, want)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("device created_at is zero")
	}
}
