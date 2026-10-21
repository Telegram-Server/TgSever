package storetest

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// DeviceLimitFixture 是同一份底层数据上的 UserStore、AuthKeyStore、AuthorizationStore，
// 以及同一个授权 store 的设备上限读写面。NewPhone 每次返回一个还没有人占用的手机号；
// NewBot 建一个 bot 账号（各实现建 bot 的方式不同）。
type DeviceLimitFixture struct {
	Users        store.UserStore
	AuthKeys     store.AuthKeyStore
	Auths        store.AuthorizationStore
	DeviceLimits store.DeviceLimitStore
	NewPhone     func(t *testing.T) string
	NewBot       func(t *testing.T) domain.User
}

// DeviceLimitContract 是设备上限的存储契约（#105）：读、设、清；非法值与不存在的账号被拒绝；
// Bind 在上限内放行、超限返回 domain.ErrDeviceLimitReached 且不写入；同一个 auth key 重新
// 绑定、password_pending 授权都不占名额；bot 不受限；并发绑定时"计数 + 插入"原子，任何
// 时刻都不突破上限。2FA 转正（MarkPasswordPassed，#106）同样检查上限：超限返回
// domain.ErrDeviceLimitReached 并删除这条待完成的授权；并发转正同样原子。
func DeviceLimitContract(t *testing.T, newFixture func(t *testing.T) DeviceLimitFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("get set clear", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		assertDeviceLimit(t, f, u.ID, 0, false)
		if err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, 3); err != nil {
			t.Fatalf("set device limit: %v", err)
		}
		assertDeviceLimit(t, f, u.ID, 3, true)
		if err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, domain.MaxDeviceLimit); err != nil {
			t.Fatalf("replace device limit: %v", err)
		}
		assertDeviceLimit(t, f, u.ID, domain.MaxDeviceLimit, true)
		cleared, err := f.DeviceLimits.ClearDeviceLimit(ctx, u.ID)
		if err != nil || !cleared {
			t.Fatalf("clear device limit cleared=%v err=%v, want true", cleared, err)
		}
		assertDeviceLimit(t, f, u.ID, 0, false)
		cleared, err = f.DeviceLimits.ClearDeviceLimit(ctx, u.ID)
		if err != nil || cleared {
			t.Fatalf("second clear cleared=%v err=%v, want false", cleared, err)
		}
	})

	t.Run("set rejects invalid values and missing users", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		for _, limit := range []int{0, -1, domain.MaxDeviceLimit + 1} {
			if err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, limit); !errors.Is(err, domain.ErrDeviceLimitInvalid) {
				t.Fatalf("set device limit %d err=%v, want ErrDeviceLimitInvalid", limit, err)
			}
		}
		assertDeviceLimit(t, f, u.ID, 0, false)
		const missingUserID = int64(1) << 60
		if err := f.DeviceLimits.SetDeviceLimit(ctx, missingUserID, 1); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("set device limit on missing user err=%v, want ErrUserNotFound", err)
		}
	})

	t.Run("bind enforces the limit without writing", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		if err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, 2); err != nil {
			t.Fatalf("set device limit: %v", err)
		}
		first, second, third := newDeviceLimitKey(t, f), newDeviceLimitKey(t, f), newDeviceLimitKey(t, f)
		bindDevice(t, f, u.ID, first, false)
		bindDevice(t, f, u.ID, second, false)
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: third, UserID: u.ID}); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("bind over the limit err=%v, want ErrDeviceLimitReached", err)
		}
		if _, found, err := f.Auths.ByAuthKey(ctx, third); err != nil || found {
			t.Fatalf("rejected key found=%v err=%v, want no authorization", found, err)
		}
		// 同一个 auth key 重新绑定覆盖原来那一行，不算新设备。
		bindDevice(t, f, u.ID, first, false)
		assertDeviceCount(t, f, u.ID, 2)

		if _, err := f.DeviceLimits.ClearDeviceLimit(ctx, u.ID); err != nil {
			t.Fatalf("clear device limit: %v", err)
		}
		bindDevice(t, f, u.ID, third, false)
		assertDeviceCount(t, f, u.ID, 3)
	})

	t.Run("password pending authorizations take no slot", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		if err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, 1); err != nil {
			t.Fatalf("set device limit: %v", err)
		}
		pending, done, extra := newDeviceLimitKey(t, f), newDeviceLimitKey(t, f), newDeviceLimitKey(t, f)
		bindDevice(t, f, u.ID, pending, true)
		bindDevice(t, f, u.ID, done, false)
		// 名额已满时，停在 2FA 步骤的授权也在绑定这一步就被拒绝。
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: extra, UserID: u.ID, PasswordPending: true}); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("pending bind at the limit err=%v, want ErrDeviceLimitReached", err)
		}
	})

	t.Run("promoting a pending authorization over the limit rejects and deletes it", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		if err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, 1); err != nil {
			t.Fatalf("set device limit: %v", err)
		}
		first, second := newDeviceLimitKey(t, f), newDeviceLimitKey(t, f)
		bindDevice(t, f, u.ID, first, true)
		bindDevice(t, f, u.ID, second, true)
		if err := f.Auths.MarkPasswordPassed(ctx, first, u.ID); err != nil {
			t.Fatalf("promote first pending authorization: %v", err)
		}
		if err := f.Auths.MarkPasswordPassed(ctx, second, u.ID); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("promote over the limit err=%v, want ErrDeviceLimitReached", err)
		}
		if _, found, err := f.Auths.ByAuthKey(ctx, second); err != nil || found {
			t.Fatalf("rejected pending authorization found=%v err=%v, want deleted", found, err)
		}
		got, found, err := f.Auths.ByAuthKey(ctx, first)
		if err != nil || !found || got.PasswordPending {
			t.Fatalf("promoted authorization=%+v found=%v err=%v, want completed", got, found, err)
		}
		// 同一 auth key 的状态不符仍是 ErrAuthorizationStateChanged，不被上限检查吞掉。
		if err := f.Auths.MarkPasswordPassed(ctx, first, u.ID); !errors.Is(err, store.ErrAuthorizationStateChanged) {
			t.Fatalf("promote an already completed authorization err=%v, want ErrAuthorizationStateChanged", err)
		}
	})

	t.Run("check reports a reached limit without writing", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		first, pending, next := newDeviceLimitKey(t, f), newDeviceLimitKey(t, f), newDeviceLimitKey(t, f)
		bindDevice(t, f, u.ID, first, false)
		bindDevice(t, f, u.ID, pending, true)
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, next); err != nil {
			t.Fatalf("check without a limit: %v", err)
		}
		if err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, 1); err != nil {
			t.Fatalf("set device limit: %v", err)
		}
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, next); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("check for a new key at the limit err=%v, want ErrDeviceLimitReached", err)
		}
		// 待完成的授权自己转正：名额已被 first 占满，同样超限。
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, pending); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("check for the pending key at the limit err=%v, want ErrDeviceLimitReached", err)
		}
		// 已占名额的 auth key 自身不算。
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, first); err != nil {
			t.Fatalf("check for the key already holding the slot: %v", err)
		}
		// 只读：pending 授权仍在、仍待完成。
		got, found, err := f.Auths.ByAuthKey(ctx, pending)
		if err != nil || !found || !got.PasswordPending {
			t.Fatalf("pending authorization after check=%+v found=%v err=%v, want unchanged", got, found, err)
		}

		bot := f.NewBot(t)
		bindDevice(t, f, bot.ID, newDeviceLimitKey(t, f), false)
		if err := f.DeviceLimits.SetDeviceLimit(ctx, bot.ID, 1); err != nil {
			t.Fatalf("set device limit on bot: %v", err)
		}
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, bot.ID, newDeviceLimitKey(t, f)); err != nil {
			t.Fatalf("check for a bot over its limit: %v", err)
		}
	})

	t.Run("promoting without a limit or for a bot is not limited", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		bindDevice(t, f, u.ID, newDeviceLimitKey(t, f), false)
		pending := newDeviceLimitKey(t, f)
		bindDevice(t, f, u.ID, pending, true)
		if err := f.Auths.MarkPasswordPassed(ctx, pending, u.ID); err != nil {
			t.Fatalf("promote without a limit: %v", err)
		}

		bot := f.NewBot(t)
		bindDevice(t, f, bot.ID, newDeviceLimitKey(t, f), false)
		botPending := newDeviceLimitKey(t, f)
		bindDevice(t, f, bot.ID, botPending, true)
		if err := f.DeviceLimits.SetDeviceLimit(ctx, bot.ID, 1); err != nil {
			t.Fatalf("set device limit on bot: %v", err)
		}
		if err := f.Auths.MarkPasswordPassed(ctx, botPending, bot.ID); err != nil {
			t.Fatalf("promote for a bot over its limit: %v", err)
		}
	})

	t.Run("concurrent promotes never exceed the limit", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		const limit, attempts = 2, 10
		if err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, limit); err != nil {
			t.Fatalf("set device limit: %v", err)
		}
		keys := make([][8]byte, attempts)
		for i := range keys {
			keys[i] = newDeviceLimitKey(t, f)
			bindDevice(t, f, u.ID, keys[i], true)
		}
		start := make(chan struct{})
		errs := make([]error, attempts)
		var wg sync.WaitGroup
		for i := range keys {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = f.Auths.MarkPasswordPassed(ctx, keys[i], u.ID)
			}(i)
		}
		close(start)
		wg.Wait()
		promoted := 0
		for i, err := range errs {
			switch {
			case err == nil:
				promoted++
			case errors.Is(err, domain.ErrDeviceLimitReached):
			default:
				t.Fatalf("concurrent promote %d: %v", i, err)
			}
		}
		if promoted != limit {
			t.Fatalf("concurrent promotes succeeded=%d, want exactly %d", promoted, limit)
		}
		// 被拒绝的待完成授权都已删除，只剩转正的那几条。
		assertDeviceCount(t, f, u.ID, limit)
	})

	t.Run("bots are not limited", func(t *testing.T) {
		f := newFixture(t)
		bot := f.NewBot(t)
		if err := f.DeviceLimits.SetDeviceLimit(ctx, bot.ID, 1); err != nil {
			t.Fatalf("set device limit on bot: %v", err)
		}
		bindDevice(t, f, bot.ID, newDeviceLimitKey(t, f), false)
		bindDevice(t, f, bot.ID, newDeviceLimitKey(t, f), false)
		assertDeviceCount(t, f, bot.ID, 2)
	})

	t.Run("concurrent binds never exceed the limit", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		const limit, attempts = 3, 12
		if err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, limit); err != nil {
			t.Fatalf("set device limit: %v", err)
		}
		keys := make([][8]byte, attempts)
		for i := range keys {
			keys[i] = newDeviceLimitKey(t, f)
		}
		start := make(chan struct{})
		errs := make([]error, attempts)
		var wg sync.WaitGroup
		for i := range keys {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: keys[i], UserID: u.ID})
			}(i)
		}
		close(start)
		wg.Wait()
		bound := 0
		for i, err := range errs {
			switch {
			case err == nil:
				bound++
			case errors.Is(err, domain.ErrDeviceLimitReached):
			default:
				t.Fatalf("concurrent bind %d: %v", i, err)
			}
		}
		if bound != limit {
			t.Fatalf("concurrent binds succeeded=%d, want exactly %d", bound, limit)
		}
		assertDeviceCount(t, f, u.ID, limit)
	})
}

func createDeviceLimitUser(t *testing.T, f DeviceLimitFixture) domain.User {
	t.Helper()
	created, err := f.Users.Create(context.Background(), domain.User{AccessHash: 1, FirstName: "Device", Phone: f.NewPhone(t)})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created
}

func newDeviceLimitKey(t *testing.T, f DeviceLimitFixture) [8]byte {
	t.Helper()
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatalf("random auth key id: %v", err)
	}
	ctx := context.Background()
	if err := f.AuthKeys.Save(ctx, store.AuthKeyData{ID: id, ServerSalt: 1}); err != nil {
		t.Fatalf("save auth key %x: %v", id, err)
	}
	t.Cleanup(func() { _ = f.AuthKeys.Delete(context.Background(), id) })
	return id
}

func bindDevice(t *testing.T, f DeviceLimitFixture, userID int64, key [8]byte, pending bool) {
	t.Helper()
	if err := f.Auths.Bind(context.Background(), domain.Authorization{AuthKeyID: key, UserID: userID, PasswordPending: pending}); err != nil {
		t.Fatalf("bind %x (pending=%v): %v", key, pending, err)
	}
}

func assertDeviceLimit(t *testing.T, f DeviceLimitFixture, userID int64, want int, wantFound bool) {
	t.Helper()
	got, found, err := f.DeviceLimits.GetDeviceLimit(context.Background(), userID)
	if err != nil || found != wantFound || got != want {
		t.Fatalf("device limit=%d found=%v err=%v, want %d found=%v", got, found, err, want, wantFound)
	}
}

func assertDeviceCount(t *testing.T, f DeviceLimitFixture, userID int64, want int) {
	t.Helper()
	got, err := f.Auths.ListByUser(context.Background(), userID)
	if err != nil {
		t.Fatalf("list authorizations: %v", err)
	}
	if len(got) != want {
		t.Fatalf("authorizations=%d, want %d", len(got), want)
	}
}
