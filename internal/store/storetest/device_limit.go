package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// DeviceLimitFixture 是同一份底层数据上的 UserStore、AuthKeyStore、AuthorizationStore，
// 以及同一个授权 store 的设备上限读写面。NewPhone 每次返回一个还没有人占用的手机号；
// NewBot 建一个 bot 账号（各实现建 bot 的方式不同）；DeleteAccount 走该实现的账号注销
// （墓碑 + 撤销授权），用来验证设备绑定随账号一起删除。
type DeviceLimitFixture struct {
	Users         store.UserStore
	AuthKeys      store.AuthKeyStore
	Auths         store.AuthorizationStore
	DeviceLimits  store.DeviceLimitStore
	NewPhone      func(t *testing.T) string
	NewBot        func(t *testing.T) domain.User
	DeleteAccount func(t *testing.T, userID int64)
}

// DeviceLimitContract 是设备上限与设备绑定的存储契约（#105、#106、#112）：读、设、清；
// 非法值与不存在的账号被拒绝；Bind 与 2FA 转正（MarkPasswordPassed）按 store.DeviceLimitStore
// 文档里的"绑定规则"放行、绑定或拒绝，拒绝时不写入任何行（转正被拒时删除这条待完成的授权）；
// 只读预检 CheckDeviceLimit 与执行点同一套规则；退出登录、撤销授权、调低或清除上限都不改
// 绑定名单，账号注销时名单一起删除；后台解绑（UnbindDevice）释放名额并撤销该设备上的授权；并发争抢最后一个名额时绑定数永不超过上限。
func DeviceLimitContract(t *testing.T, newFixture func(t *testing.T) DeviceLimitFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("get set clear", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		assertDeviceLimit(t, f, u.ID, 0, false)
		if _, err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, 3); err != nil {
			t.Fatalf("set device limit: %v", err)
		}
		assertDeviceLimit(t, f, u.ID, 3, true)
		if _, err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, domain.MaxDeviceLimit); err != nil {
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
			if _, err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, limit); !errors.Is(err, domain.ErrDeviceLimitInvalid) {
				t.Fatalf("set device limit %d err=%v, want ErrDeviceLimitInvalid", limit, err)
			}
		}
		assertDeviceLimit(t, f, u.ID, 0, false)
		const missingUserID = int64(1) << 60
		if _, err := f.DeviceLimits.SetDeviceLimit(ctx, missingUserID, 1); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("set device limit on missing user err=%v, want ErrUserNotFound", err)
		}
	})

	t.Run("without a limit every device signs in and nothing is bound", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, ""), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "free-a"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "free-b"), false)
		assertDeviceCount(t, f, u.ID, 3)
		assertBoundDevices(t, f, u.ID)
	})

	t.Run("bots are not limited and nothing is bound", func(t *testing.T) {
		f := newFixture(t)
		bot := f.NewBot(t)
		if _, err := f.DeviceLimits.SetDeviceLimit(ctx, bot.ID, 1); err != nil {
			t.Fatalf("set device limit on bot: %v", err)
		}
		bindDevice(t, f, bot.ID, newDeviceKey(t, f, ""), false)
		bindDevice(t, f, bot.ID, newDeviceKey(t, f, "bot-a"), false)
		bindDevice(t, f, bot.ID, newDeviceKey(t, f, "bot-b"), false)
		assertDeviceCount(t, f, bot.ID, 3)
		assertBoundDevices(t, f, bot.ID)
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, bot.ID, newDeviceKey(t, f, "")); err != nil {
			t.Fatalf("check for a bot over its limit: %v", err)
		}
	})

	t.Run("a device without a device id is rejected without writing", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 2)
		key := newDeviceKey(t, f, "")
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: key, UserID: u.ID}); !errors.Is(err, domain.ErrDeviceIDRequired) {
			t.Fatalf("bind without a device id err=%v, want ErrDeviceIDRequired", err)
		}
		assertNoAuthorization(t, f, key)
		assertBoundDevices(t, f, u.ID)
	})

	t.Run("binding records the device and a full list rejects a new one", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 1)
		first := newDeviceKey(t, f, "phone-1")
		if err := f.Auths.Bind(ctx, domain.Authorization{
			AuthKeyID: first, UserID: u.ID, DeviceModel: "Pixel 9", Platform: "Android", AppVersion: "11.2",
		}); err != nil {
			t.Fatalf("bind the first device: %v", err)
		}
		got := listBindings(t, f, u.ID)
		if len(got) != 1 {
			t.Fatalf("bindings=%+v, want one", got)
		}
		b := got[0]
		if b.UserID != u.ID || b.DeviceID != "phone-1" || b.DeviceModel != "Pixel 9" || b.Platform != "Android" || b.AppVersion != "11.2" {
			t.Fatalf("binding=%+v, want phone-1 Pixel 9 / Android / 11.2", b)
		}
		if b.BoundAt.IsZero() || b.LastUsedAt.Before(b.BoundAt) {
			t.Fatalf("binding times bound=%v last used=%v, want set and ordered", b.BoundAt, b.LastUsedAt)
		}
		next := newDeviceKey(t, f, "phone-2")
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: next, UserID: u.ID}); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("bind a new device at the limit err=%v, want ErrDeviceLimitReached", err)
		}
		assertNoAuthorization(t, f, next)
		assertBoundDevices(t, f, u.ID, "phone-1")
	})

	t.Run("a bound device signs in again after logging out", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 1)
		first := newDeviceKey(t, f, "laptop")
		bindDevice(t, f, u.ID, first, false)
		before := onlyBinding(t, f, u.ID)
		if err := f.Auths.Delete(ctx, first); err != nil {
			t.Fatalf("log out: %v", err)
		}
		assertBoundDevices(t, f, u.ID, "laptop")
		time.Sleep(20 * time.Millisecond)
		// 卸载重装后是新的 auth key，但还是同一个 device_id。
		reinstalled := newDeviceKey(t, f, "laptop")
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: reinstalled, UserID: u.ID, DeviceModel: "Other"}); err != nil {
			t.Fatalf("bound device signs in again: %v", err)
		}
		after := listBindings(t, f, u.ID)
		if len(after) != 1 || !after[0].LastUsedAt.After(before.LastUsedAt) {
			t.Fatalf("bindings after re-login=%+v, want one with a newer last used than %v", after, before.LastUsedAt)
		}
		if !after[0].BoundAt.Equal(before.BoundAt) || after[0].DeviceModel != before.DeviceModel {
			t.Fatalf("re-login rewrote the binding: before=%+v after=%+v", before, after[0])
		}
	})

	t.Run("one device id with several authorizations takes one slot", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 2)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "desk"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "desk"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "desk"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "tablet"), false)
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: newDeviceKey(t, f, "third"), UserID: u.ID}); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("third device err=%v, want ErrDeviceLimitReached", err)
		}
		assertDeviceCount(t, f, u.ID, 4)
		assertBoundDevices(t, f, u.ID, "desk", "tablet")
	})

	t.Run("password pending authorizations are not checked or bound", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 1)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "owner"), false)
		// 名额已满、甚至没有 device_id，停在 2FA 的授权也先放行，转正时再判。
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "stranger"), true)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, ""), true)
		assertDeviceCount(t, f, u.ID, 3)
		assertBoundDevices(t, f, u.ID, "owner")
	})

	t.Run("promotion binds or rejects and deletes the pending authorization", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 1)
		noID, first, second, again := newDeviceKey(t, f, ""), newDeviceKey(t, f, "first"), newDeviceKey(t, f, "second"), newDeviceKey(t, f, "first")
		for _, key := range [][8]byte{noID, first, second, again} {
			bindDevice(t, f, u.ID, key, true)
		}
		if err := f.Auths.MarkPasswordPassed(ctx, noID, u.ID); !errors.Is(err, domain.ErrDeviceIDRequired) {
			t.Fatalf("promote without a device id err=%v, want ErrDeviceIDRequired", err)
		}
		assertNoAuthorization(t, f, noID)
		if err := f.Auths.MarkPasswordPassed(ctx, first, u.ID); err != nil {
			t.Fatalf("promote the first device: %v", err)
		}
		assertBoundDevices(t, f, u.ID, "first")
		if err := f.Auths.MarkPasswordPassed(ctx, second, u.ID); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("promote a new device at the limit err=%v, want ErrDeviceLimitReached", err)
		}
		assertNoAuthorization(t, f, second)
		// 已绑定的设备换一个 auth key 转正仍然放行。
		if err := f.Auths.MarkPasswordPassed(ctx, again, u.ID); err != nil {
			t.Fatalf("promote the bound device on another key: %v", err)
		}
		assertBoundDevices(t, f, u.ID, "first")
		got, found, err := f.Auths.ByAuthKey(ctx, first)
		if err != nil || !found || got.PasswordPending {
			t.Fatalf("promoted authorization=%+v found=%v err=%v, want completed", got, found, err)
		}
		// 同一 auth key 的状态不符仍是 ErrAuthorizationStateChanged，不被绑定规则吞掉。
		if err := f.Auths.MarkPasswordPassed(ctx, first, u.ID); !errors.Is(err, store.ErrAuthorizationStateChanged) {
			t.Fatalf("promote an already completed authorization err=%v, want ErrAuthorizationStateChanged", err)
		}
		if err := f.Auths.MarkPasswordPassed(ctx, second, u.ID); !errors.Is(err, store.ErrAuthorizationStateChanged) {
			t.Fatalf("promote a deleted authorization err=%v, want ErrAuthorizationStateChanged", err)
		}
	})

	t.Run("promoting without a limit or for a bot binds nothing", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		pending := newDeviceKey(t, f, "")
		bindDevice(t, f, u.ID, pending, true)
		if err := f.Auths.MarkPasswordPassed(ctx, pending, u.ID); err != nil {
			t.Fatalf("promote without a limit: %v", err)
		}
		assertBoundDevices(t, f, u.ID)

		bot := f.NewBot(t)
		botPending := newDeviceKey(t, f, "")
		bindDevice(t, f, bot.ID, botPending, true)
		if _, err := f.DeviceLimits.SetDeviceLimit(ctx, bot.ID, 1); err != nil {
			t.Fatalf("set device limit on bot: %v", err)
		}
		if err := f.Auths.MarkPasswordPassed(ctx, botPending, bot.ID); err != nil {
			t.Fatalf("promote for a bot over its limit: %v", err)
		}
		assertBoundDevices(t, f, bot.ID)
	})

	t.Run("check follows the bind rules without writing", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		noID := newDeviceKey(t, f, "")
		fresh := newDeviceKey(t, f, "fresh")
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, noID); err != nil {
			t.Fatalf("check without a limit: %v", err)
		}
		setDeviceLimit(t, f, u.ID, 1)
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, noID); !errors.Is(err, domain.ErrDeviceIDRequired) {
			t.Fatalf("check without a device id err=%v, want ErrDeviceIDRequired", err)
		}
		// 有空位：放行，但只读，不写绑定。
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, fresh); err != nil {
			t.Fatalf("check with a free slot: %v", err)
		}
		assertBoundDevices(t, f, u.ID)

		bindDevice(t, f, u.ID, newDeviceKey(t, f, "owner"), false)
		before := onlyBinding(t, f, u.ID)
		pending := newDeviceKey(t, f, "stranger")
		bindDevice(t, f, u.ID, pending, true)
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, fresh); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("check a new device at the limit err=%v, want ErrDeviceLimitReached", err)
		}
		// 待完成的授权预检的是"转正之后"：新设备同样超限。
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, pending); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("check the pending new device at the limit err=%v, want ErrDeviceLimitReached", err)
		}
		// 已绑定设备的另一把 auth key 放行，且不刷新最近使用时间。
		if err := f.DeviceLimits.CheckDeviceLimit(ctx, u.ID, newDeviceKey(t, f, "owner")); err != nil {
			t.Fatalf("check the bound device on another key: %v", err)
		}
		after := listBindings(t, f, u.ID)
		if len(after) != 1 || !after[0].LastUsedAt.Equal(before.LastUsedAt) {
			t.Fatalf("bindings after check=%+v, want unchanged %+v", after, before)
		}
		got, found, err := f.Auths.ByAuthKey(ctx, pending)
		if err != nil || !found || !got.PasswordPending {
			t.Fatalf("pending authorization after check=%+v found=%v err=%v, want unchanged", got, found, err)
		}
		// 与执行点一致：预检说超限的，转正同样被拒。
		if err := f.Auths.MarkPasswordPassed(ctx, pending, u.ID); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("promote after a reached check err=%v, want ErrDeviceLimitReached", err)
		}
	})

	t.Run("lowering the limit keeps the bound devices", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 3)
		keys := map[string][8]byte{}
		for _, id := range []string{"a", "b", "c"} {
			keys[id] = newDeviceKey(t, f, id)
			bindDevice(t, f, u.ID, keys[id], false)
		}
		setDeviceLimit(t, f, u.ID, 1)
		assertBoundDevices(t, f, u.ID, "a", "b", "c")
		assertDeviceCount(t, f, u.ID, 3)
		// 超出新上限的已绑定设备仍能重新登录；新设备被拒。
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "c"), false)
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: newDeviceKey(t, f, "d"), UserID: u.ID}); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("new device over the lowered limit err=%v, want ErrDeviceLimitReached", err)
		}
		assertBoundDevices(t, f, u.ID, "a", "b", "c")
	})

	t.Run("clearing the limit keeps the bindings for a later limit", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 1)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "kept"), false)
		if _, err := f.DeviceLimits.ClearDeviceLimit(ctx, u.ID); err != nil {
			t.Fatalf("clear device limit: %v", err)
		}
		assertBoundDevices(t, f, u.ID, "kept")
		// 不限制期间登录的设备不被记录。
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "unlimited"), false)
		assertBoundDevices(t, f, u.ID, "kept")
		setDeviceLimit(t, f, u.ID, 1)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "kept"), false)
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: newDeviceKey(t, f, "unlimited"), UserID: u.ID}); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("unbound device under the restored limit err=%v, want ErrDeviceLimitReached", err)
		}
	})

	t.Run("logging out and revoking keep the bindings", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 3)
		a, b, c := newDeviceKey(t, f, "a"), newDeviceKey(t, f, "b"), newDeviceKey(t, f, "c")
		for _, key := range [][8]byte{a, b, c} {
			bindDevice(t, f, u.ID, key, false)
		}
		if err := f.Auths.Delete(ctx, a); err != nil {
			t.Fatalf("log out: %v", err)
		}
		auth, found, err := f.Auths.ByAuthKey(ctx, b)
		if err != nil || !found {
			t.Fatalf("read authorization b found=%v err=%v", found, err)
		}
		if _, found, err := f.Auths.DeleteByHash(ctx, u.ID, auth.Hash); err != nil || !found {
			t.Fatalf("revoke by hash found=%v err=%v", found, err)
		}
		if _, err := f.Auths.DeleteByUserExcept(ctx, u.ID, [8]byte{}); err != nil {
			t.Fatalf("reset authorizations: %v", err)
		}
		assertDeviceCount(t, f, u.ID, 0)
		assertBoundDevices(t, f, u.ID, "a", "b", "c")
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: newDeviceKey(t, f, "d"), UserID: u.ID}); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("new device after revoking everything err=%v, want ErrDeviceLimitReached", err)
		}
	})

	t.Run("unbinding frees the slot and revokes every authorization on that device", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		other := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 2)
		setDeviceLimit(t, f, other.ID, 2)
		old1, old2 := newDeviceKey(t, f, "old"), newDeviceKey(t, f, "old")
		pending := newDeviceKey(t, f, "old")
		kept := newDeviceKey(t, f, "kept")
		othersOld := newDeviceKey(t, f, "old")
		bindDevice(t, f, u.ID, old1, false)
		bindDevice(t, f, u.ID, old2, false)
		bindDevice(t, f, u.ID, kept, false)
		bindDevice(t, f, u.ID, pending, true)
		bindDevice(t, f, other.ID, othersOld, false)
		if err := f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: newDeviceKey(t, f, "new"), UserID: u.ID}); !errors.Is(err, domain.ErrDeviceLimitReached) {
			t.Fatalf("new device on a full list err=%v, want ErrDeviceLimitReached", err)
		}

		if n, err := f.DeviceLimits.PreviewUnbindDevice(ctx, u.ID, "old"); err != nil || n != 3 {
			t.Fatalf("preview unbind = %d, %v; want 3 authorizations", n, err)
		}
		assertBoundDevices(t, f, u.ID, "old", "kept")

		revoked, err := f.DeviceLimits.UnbindDevice(ctx, u.ID, "old")
		if err != nil {
			t.Fatalf("unbind: %v", err)
		}
		got := map[[8]byte]bool{}
		for _, a := range revoked {
			if a.UserID != u.ID {
				t.Fatalf("revoked authorization of user %d, want %d", a.UserID, u.ID)
			}
			got[a.AuthKeyID] = true
		}
		if len(revoked) != 3 || !got[old1] || !got[old2] || !got[pending] {
			t.Fatalf("revoked=%+v, want the three authorizations on device old", revoked)
		}
		for _, key := range [][8]byte{old1, old2, pending} {
			assertNoAuthorization(t, f, key)
			// 与远程踢设备一样保留协议 key，客户端重连后才能收到 401。
			if _, found, err := f.AuthKeys.Get(ctx, key); err != nil || !found {
				t.Fatalf("protocol key %x found=%v err=%v, want kept", key, found, err)
			}
		}
		if _, found, err := f.Auths.ByAuthKey(ctx, kept); err != nil || !found {
			t.Fatalf("authorization on the other bound device found=%v err=%v, want kept", found, err)
		}
		if _, found, err := f.Auths.ByAuthKey(ctx, othersOld); err != nil || !found {
			t.Fatalf("another account's authorization on the same device id found=%v err=%v, want kept", found, err)
		}
		assertBoundDevices(t, f, u.ID, "kept")
		assertBoundDevices(t, f, other.ID, "old")
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "new"), false)
		assertBoundDevices(t, f, u.ID, "kept", "new")
	})

	t.Run("a session copied to another device keeps its bound device id and is revoked by unbinding it", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 1)
		key := newDeviceKey(t, f, "original")
		bindDevice(t, f, u.ID, key, false)
		assertBoundDevices(t, f, u.ID, "original")

		// 同一把 perm key 被拷到另一台机器，那里的 initConnection 上报了别的 device_id。
		reportDeviceID(t, f, key, "copied")
		if got, found, err := f.AuthKeys.Revalidate(ctx, key); err != nil || !found || got.DeviceID != "original" {
			t.Fatalf("device_id=%q found=%v err=%v, want original kept", got.DeviceID, found, err)
		}
		if backfill(t, f, key) {
			t.Fatalf("backfill after a copied session reported another device=true, want false")
		}
		assertBoundDevices(t, f, u.ID, "original")

		revoked, err := f.DeviceLimits.UnbindDevice(ctx, u.ID, "original")
		if err != nil {
			t.Fatalf("unbind: %v", err)
		}
		if len(revoked) != 1 || revoked[0].AuthKeyID != key {
			t.Fatalf("revoked=%+v, want the copied session", revoked)
		}
		assertNoAuthorization(t, f, key)
		assertBoundDevices(t, f, u.ID)
	})

	t.Run("unbinding a device that is not bound changes nothing", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		// 设上限之前登录、还没有绑定的设备：有授权，但不在名单里。
		unbound := newDeviceKey(t, f, "unbound")
		bindDevice(t, f, u.ID, unbound, false)
		if _, err := f.DeviceLimits.PreviewUnbindDevice(ctx, u.ID, "unbound"); !errors.Is(err, domain.ErrDeviceBindingNotFound) {
			t.Fatalf("preview unbind err=%v, want ErrDeviceBindingNotFound", err)
		}
		if revoked, err := f.DeviceLimits.UnbindDevice(ctx, u.ID, "unbound"); !errors.Is(err, domain.ErrDeviceBindingNotFound) || len(revoked) != 0 {
			t.Fatalf("unbind = %+v, %v; want ErrDeviceBindingNotFound", revoked, err)
		}
		if _, found, err := f.Auths.ByAuthKey(ctx, unbound); err != nil || !found {
			t.Fatalf("authorization found=%v err=%v, want kept", found, err)
		}
		// 设上限时补绑进名单；解绑一次之后再解绑同一台设备就是"不存在"。
		setDeviceLimit(t, f, u.ID, 2)
		assertBoundDevices(t, f, u.ID, "unbound")
		if _, err := f.DeviceLimits.UnbindDevice(ctx, u.ID, "unbound"); err != nil {
			t.Fatalf("first unbind: %v", err)
		}
		if _, err := f.DeviceLimits.UnbindDevice(ctx, u.ID, "unbound"); !errors.Is(err, domain.ErrDeviceBindingNotFound) {
			t.Fatalf("second unbind err=%v, want ErrDeviceBindingNotFound", err)
		}
	})

	t.Run("deleting the account removes its bindings", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		other := createDeviceLimitUser(t, f)
		setDeviceLimit(t, f, u.ID, 2)
		setDeviceLimit(t, f, other.ID, 2)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "gone-a"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "gone-b"), false)
		bindDevice(t, f, other.ID, newDeviceKey(t, f, "gone-a"), false)
		f.DeleteAccount(t, u.ID)
		assertBoundDevices(t, f, u.ID)
		assertBoundDevices(t, f, other.ID, "gone-a")
	})

	t.Run("setting the limit backfills signed-in devices in login order", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		// 上限之前登录的设备：按登录先后 a、无 device_id、停在 2FA 的 p、b、a 的另一把 key、c。
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "a"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, ""), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "p"), true)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "b"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "a"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "c"), false)
		assertBoundDevices(t, f, u.ID)

		if n := setDeviceLimitBackfilled(t, f, u.ID, 2); n != 2 {
			t.Fatalf("backfilled=%d, want 2", n)
		}
		assertBoundDevices(t, f, u.ID, "a", "b")
		// 没有 device_id、超出名额或停在 2FA 的设备保持登录，只是不绑定。
		assertDeviceCount(t, f, u.ID, 6)

		// 调高上限：已绑定的不重复计，继续按登录先后补到上限。
		if n := setDeviceLimitBackfilled(t, f, u.ID, 5); n != 1 {
			t.Fatalf("backfilled after raising=%d, want 1", n)
		}
		assertBoundDevices(t, f, u.ID, "a", "b", "c")
		if n := setDeviceLimitBackfilled(t, f, u.ID, 5); n != 0 {
			t.Fatalf("backfilled on an unchanged limit=%d, want 0", n)
		}
		assertDeviceCount(t, f, u.ID, 6)
	})

	t.Run("setting a limit on a bot backfills nothing", func(t *testing.T) {
		f := newFixture(t)
		bot := f.NewBot(t)
		bindDevice(t, f, bot.ID, newDeviceKey(t, f, "bot-device"), false)
		if n := setDeviceLimitBackfilled(t, f, bot.ID, 1); n != 0 {
			t.Fatalf("backfilled for a bot=%d, want 0", n)
		}
		assertBoundDevices(t, f, bot.ID)
	})

	t.Run("previewing the backfill counts without writing", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "a"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, ""), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "a"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "b"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "c"), false)
		for limit, want := range map[int]int{1: 1, 2: 2, 3: 3, 10: 3} {
			if n := previewBackfill(t, f, u.ID, limit); n != want {
				t.Fatalf("preview with limit %d=%d, want %d", limit, n, want)
			}
		}
		assertDeviceLimit(t, f, u.ID, 0, false)
		assertBoundDevices(t, f, u.ID)
		// 预览与执行一致，已绑定的设备不再计入。
		if n := setDeviceLimitBackfilled(t, f, u.ID, 2); n != 2 {
			t.Fatalf("backfilled=%d, want 2", n)
		}
		if n := previewBackfill(t, f, u.ID, 2); n != 0 {
			t.Fatalf("preview at an already filled limit=%d, want 0", n)
		}
		if n := previewBackfill(t, f, u.ID, 4); n != 1 {
			t.Fatalf("preview of raising the limit=%d, want 1", n)
		}
		assertBoundDevices(t, f, u.ID, "a", "b")

		bot := f.NewBot(t)
		bindDevice(t, f, bot.ID, newDeviceKey(t, f, "bot-device"), false)
		if n := previewBackfill(t, f, bot.ID, 1); n != 0 {
			t.Fatalf("preview for a bot=%d, want 0", n)
		}
	})

	t.Run("a device id reported after signing in is backfilled while a slot is free", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		// 老客户端在设上限之前登录，没有 device_id。
		k1, k2, k3 := newDeviceKey(t, f, ""), newDeviceKey(t, f, ""), newDeviceKey(t, f, "")
		for _, key := range [][8]byte{k1, k2, k3} {
			bindDevice(t, f, u.ID, key, false)
		}
		if n := setDeviceLimitBackfilled(t, f, u.ID, 2); n != 0 {
			t.Fatalf("backfilled without device ids=%d, want 0", n)
		}
		reportDeviceID(t, f, k1, "upgraded-1")
		if !backfill(t, f, k1) {
			t.Fatalf("backfill of the first upgraded device=false, want bound")
		}
		// 已绑定的设备再次上报不重复绑定。
		if backfill(t, f, k1) {
			t.Fatalf("second backfill of a bound device=true, want false")
		}
		reportDeviceID(t, f, k2, "upgraded-2")
		if !backfill(t, f, k2) {
			t.Fatalf("backfill of the second upgraded device=false, want bound")
		}
		// 名额已满：什么都不做，也不踢下线。
		reportDeviceID(t, f, k3, "upgraded-3")
		if backfill(t, f, k3) {
			t.Fatalf("backfill over the limit=true, want false")
		}
		assertBoundDevices(t, f, u.ID, "upgraded-1", "upgraded-2")
		assertDeviceCount(t, f, u.ID, 3)
	})

	t.Run("backfill skips pending, unlimited, bot and signed-out keys", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		free := newDeviceKey(t, f, "")
		bindDevice(t, f, u.ID, free, false)
		reportDeviceID(t, f, free, "unlimited")
		if backfill(t, f, free) {
			t.Fatalf("backfill without a limit=true, want false")
		}
		assertBoundDevices(t, f, u.ID)

		setDeviceLimit(t, f, u.ID, 3)
		assertBoundDevices(t, f, u.ID, "unlimited")
		pending := newDeviceKey(t, f, "")
		bindDevice(t, f, u.ID, pending, true)
		reportDeviceID(t, f, pending, "pending")
		if backfill(t, f, pending) {
			t.Fatalf("backfill of a password pending authorization=true, want false")
		}
		if backfill(t, f, newDeviceKey(t, f, "signed-out")) {
			t.Fatalf("backfill of a key without an authorization=true, want false")
		}
		assertBoundDevices(t, f, u.ID, "unlimited")

		bot := f.NewBot(t)
		botKey := newDeviceKey(t, f, "")
		bindDevice(t, f, bot.ID, botKey, false)
		if _, err := f.DeviceLimits.SetDeviceLimit(ctx, bot.ID, 1); err != nil {
			t.Fatalf("set device limit on bot: %v", err)
		}
		reportDeviceID(t, f, botKey, "bot-device")
		if backfill(t, f, botKey) {
			t.Fatalf("backfill for a bot=true, want false")
		}
		assertBoundDevices(t, f, bot.ID)
	})

	t.Run("backfill racing new devices for the last slots never exceeds the limit", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		const limit, each = 3, 6
		old := make([][8]byte, each)
		for i := range old {
			old[i] = newDeviceKey(t, f, "")
			bindDevice(t, f, u.ID, old[i], false)
		}
		setDeviceLimit(t, f, u.ID, limit)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "held"), false)
		for i := range old {
			reportDeviceID(t, f, old[i], newDeviceIDValue(t))
		}
		fresh := make([][8]byte, each)
		for i := range fresh {
			fresh[i] = newDeviceKey(t, f, newDeviceIDValue(t))
		}
		errs := runConcurrently(2*each, func(i int) error {
			if i < each {
				_, err := f.DeviceLimits.BackfillDeviceBinding(ctx, old[i])
				return err
			}
			return f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: fresh[i-each], UserID: u.ID})
		})
		countAccepted(t, errs, "backfill or bind")
		if got := listBindings(t, f, u.ID); len(got) != limit {
			t.Fatalf("bindings=%d, want exactly %d", len(got), limit)
		}
	})

	t.Run("setting the limit while new devices sign in never exceeds it", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		const limit, each = 3, 6
		for i := 0; i < each; i++ {
			bindDevice(t, f, u.ID, newDeviceKey(t, f, newDeviceIDValue(t)), false)
		}
		fresh := make([][8]byte, each)
		for i := range fresh {
			fresh[i] = newDeviceKey(t, f, newDeviceIDValue(t))
		}
		errs := runConcurrently(each+1, func(i int) error {
			if i == each {
				_, err := f.DeviceLimits.SetDeviceLimit(ctx, u.ID, limit)
				return err
			}
			return f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: fresh[i], UserID: u.ID})
		})
		countAccepted(t, errs, "set limit or bind")
		if got := listBindings(t, f, u.ID); len(got) != limit {
			t.Fatalf("bindings=%d, want exactly %d", len(got), limit)
		}
	})

	t.Run("concurrent promotes never exceed the limit", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		const limit, attempts = 2, 10
		setDeviceLimit(t, f, u.ID, limit)
		keys := make([][8]byte, attempts)
		for i := range keys {
			keys[i] = newDeviceKey(t, f, newDeviceIDValue(t))
			bindDevice(t, f, u.ID, keys[i], true)
		}
		errs := runConcurrently(attempts, func(i int) error {
			return f.Auths.MarkPasswordPassed(ctx, keys[i], u.ID)
		})
		if promoted := countAccepted(t, errs, "promote"); promoted != limit {
			t.Fatalf("concurrent promotes succeeded=%d, want exactly %d", promoted, limit)
		}
		// 被拒绝的待完成授权都已删除，只剩转正的那几条。
		assertDeviceCount(t, f, u.ID, limit)
		if got := listBindings(t, f, u.ID); len(got) != limit {
			t.Fatalf("bindings=%d, want %d", len(got), limit)
		}
	})

	t.Run("concurrent new devices racing for the last slot bind exactly one", func(t *testing.T) {
		f := newFixture(t)
		u := createDeviceLimitUser(t, f)
		const limit, attempts = 3, 12
		setDeviceLimit(t, f, u.ID, limit)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "held-1"), false)
		bindDevice(t, f, u.ID, newDeviceKey(t, f, "held-2"), false)
		keys := make([][8]byte, attempts)
		for i := range keys {
			keys[i] = newDeviceKey(t, f, newDeviceIDValue(t))
		}
		errs := runConcurrently(attempts, func(i int) error {
			return f.Auths.Bind(ctx, domain.Authorization{AuthKeyID: keys[i], UserID: u.ID})
		})
		if bound := countAccepted(t, errs, "bind"); bound != 1 {
			t.Fatalf("concurrent binds for the last slot succeeded=%d, want exactly 1", bound)
		}
		assertDeviceCount(t, f, u.ID, limit)
		if got := listBindings(t, f, u.ID); len(got) != limit {
			t.Fatalf("bindings=%d, want %d", len(got), limit)
		}
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

func setDeviceLimit(t *testing.T, f DeviceLimitFixture, userID int64, limit int) {
	t.Helper()
	setDeviceLimitBackfilled(t, f, userID, limit)
}

// setDeviceLimitBackfilled 设上限并返回这次补绑的设备数。
func setDeviceLimitBackfilled(t *testing.T, f DeviceLimitFixture, userID int64, limit int) int {
	t.Helper()
	n, err := f.DeviceLimits.SetDeviceLimit(context.Background(), userID, limit)
	if err != nil {
		t.Fatalf("set device limit %d: %v", limit, err)
	}
	return n
}

func previewBackfill(t *testing.T, f DeviceLimitFixture, userID int64, limit int) int {
	t.Helper()
	n, err := f.DeviceLimits.PreviewDeviceLimitBackfill(context.Background(), userID, limit)
	if err != nil {
		t.Fatalf("preview backfill with limit %d: %v", limit, err)
	}
	return n
}

func backfill(t *testing.T, f DeviceLimitFixture, key [8]byte) bool {
	t.Helper()
	bound, err := f.DeviceLimits.BackfillDeviceBinding(context.Background(), key)
	if err != nil {
		t.Fatalf("backfill device binding on %x: %v", key, err)
	}
	return bound
}

// reportDeviceID 模拟老客户端升级后重连：已登录的 key 之后才持久化 device_id。
func reportDeviceID(t *testing.T, f DeviceLimitFixture, key [8]byte, deviceID string) {
	t.Helper()
	if _, err := f.AuthKeys.UpdateClientInfo(context.Background(), key, store.AuthKeyClientInfo{DeviceID: deviceID}); err != nil {
		t.Fatalf("persist device id on %x: %v", key, err)
	}
}

func newDeviceIDValue(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatalf("random device id: %v", err)
	}
	return hex.EncodeToString(raw[:])
}

// newDeviceKey 建一把 permanent auth key；deviceID 非空时像客户端上报过 initConnection.params
// 那样持久化到这把 key 上，空串表示从未上报。
func newDeviceKey(t *testing.T, f DeviceLimitFixture, deviceID string) [8]byte {
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
	if deviceID != "" {
		if _, err := f.AuthKeys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{DeviceID: deviceID}); err != nil {
			t.Fatalf("persist device id on %x: %v", id, err)
		}
	}
	return id
}

func bindDevice(t *testing.T, f DeviceLimitFixture, userID int64, key [8]byte, pending bool) {
	t.Helper()
	if err := f.Auths.Bind(context.Background(), domain.Authorization{AuthKeyID: key, UserID: userID, PasswordPending: pending}); err != nil {
		t.Fatalf("bind %x (pending=%v): %v", key, pending, err)
	}
}

func onlyBinding(t *testing.T, f DeviceLimitFixture, userID int64) domain.DeviceBinding {
	t.Helper()
	got := listBindings(t, f, userID)
	if len(got) != 1 {
		t.Fatalf("bindings=%+v, want exactly one", got)
	}
	return got[0]
}

func listBindings(t *testing.T, f DeviceLimitFixture, userID int64) []domain.DeviceBinding {
	t.Helper()
	got, err := f.DeviceLimits.ListDeviceBindings(context.Background(), userID)
	if err != nil {
		t.Fatalf("list device bindings: %v", err)
	}
	return got
}

// assertBoundDevices 断言账号的绑定名单恰好是 want（按绑定先后）。
func assertBoundDevices(t *testing.T, f DeviceLimitFixture, userID int64, want ...string) {
	t.Helper()
	got := listBindings(t, f, userID)
	ids := make([]string, 0, len(got))
	for _, b := range got {
		ids = append(ids, b.DeviceID)
	}
	if len(ids) != len(want) {
		t.Fatalf("bound devices=%v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("bound devices=%v, want %v", ids, want)
		}
	}
}

func assertNoAuthorization(t *testing.T, f DeviceLimitFixture, key [8]byte) {
	t.Helper()
	if _, found, err := f.Auths.ByAuthKey(context.Background(), key); err != nil || found {
		t.Fatalf("authorization on %x found=%v err=%v, want none", key, found, err)
	}
}

func runConcurrently(n int, fn func(i int) error) []error {
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

func countAccepted(t *testing.T, errs []error, what string) int {
	t.Helper()
	accepted := 0
	for i, err := range errs {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, domain.ErrDeviceLimitReached):
		default:
			t.Fatalf("concurrent %s %d: %v", what, i, err)
		}
	}
	return accepted
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
