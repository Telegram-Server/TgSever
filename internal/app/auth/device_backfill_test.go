package auth

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	mtcrypto "github.com/iamxvbaba/td/crypto"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 延迟补绑（#115）：受限账号已完成的授权之后才持久化 device_id（老客户端升级后重连）时，
// 有名额就补绑，没有名额什么都不做、不踢下线；未设上限的账号不产生绑定。

func (f deviceLimitFixture) reportDeviceID(t *testing.T, key [8]byte, deviceID string) {
	t.Helper()
	if err := f.svc.UpdateAuthKeyClientInfo(context.Background(), key, domain.AuthKeyClientInfo{DeviceID: deviceID}); err != nil {
		t.Fatalf("report device id %q: %v", deviceID, err)
	}
}

func TestDeviceIDReportedAfterSignInIsBackfilledWhileASlotIsFree(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105201"
	u := f.existingUser(t, phone)
	keys := [][8]byte{f.key(t, 1, ""), f.key(t, 2, ""), f.key(t, 3, "")}
	for i, key := range keys {
		if err := f.signInOn(t, phone, key); err != nil {
			t.Fatalf("old client %d signs in before the limit: %v", i+1, err)
		}
	}
	f.setLimit(t, u.ID, 2)
	f.assertBindings(t, u.ID, 0)

	f.reportDeviceID(t, keys[0], "upgraded-1")
	f.assertBindings(t, u.ID, 1)
	f.reportDeviceID(t, keys[1], "upgraded-2")
	f.assertBindings(t, u.ID, 2)
	// 名额已满：不绑定，也不踢下线。
	f.reportDeviceID(t, keys[2], "upgraded-3")
	f.assertBindings(t, u.ID, 2)
	f.assertDevices(t, u.ID, 3)
}

func TestDeviceIDReportedOnAnUnlimitedAccountBindsNothing(t *testing.T) {
	f := newDeviceLimitFixture(t)
	const phone = "15550105202"
	u := f.existingUser(t, phone)
	key := f.key(t, 1, "")
	if err := f.signInOn(t, phone, key); err != nil {
		t.Fatalf("sign in: %v", err)
	}
	f.reportDeviceID(t, key, "unlimited")
	f.assertBindings(t, u.ID, 0)
}

// failingBackfillStore 让延迟补绑失败，用来验证"尽力而为，失败只记日志"。
type failingBackfillStore struct {
	*memory.AuthorizationStore
}

var errBackfillUnavailable = errors.New("backfill unavailable")

func (failingBackfillStore) BackfillDeviceBinding(context.Context, [8]byte) (bool, error) {
	return false, errBackfillUnavailable
}

func TestDeviceBackfillFailureIsReportedNotReturned(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	key := testAuthKey(0x81)
	saveAuthKey(t, keys, key)
	var (
		observedKey [8]byte
		observedErr error
	)
	svc := NewService(memory.NewUserStore(), failingBackfillStore{memory.NewAuthorizationStore()}, memory.NewCodeStore(), keys, nil, "12345",
		WithDeviceBackfillFailureObserver(func(_ context.Context, authKeyID [8]byte, err error) {
			observedKey, observedErr = authKeyID, err
		}))

	if err := svc.UpdateAuthKeyClientInfo(ctx, key.ID, domain.AuthKeyClientInfo{DeviceID: "desk-0001"}); err != nil {
		t.Fatalf("update client info with a failing backfill: %v", err)
	}
	if observedKey != key.ID || !errors.Is(observedErr, errBackfillUnavailable) {
		t.Fatalf("observed key=%x err=%v, want %x and the backfill error", observedKey, observedErr, key.ID)
	}
	info, found, err := svc.AuthKeyClientInfo(ctx, key.ID)
	if err != nil || !found || info.DeviceID != "desk-0001" {
		t.Fatalf("device_id=%q found=%v err=%v, want it persisted despite the failed backfill", info.DeviceID, found, err)
	}
}

// PFS：temp key 上报的 device_id 在 bindTempAuthKey 时才带到已登录的 perm key 上，这条
// 路径同样触发延迟补绑。
func TestDeviceIDCarriedToAPermanentKeyIsBackfilled(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	keys := memory.NewAuthKeyStore()
	auths := memory.NewAuthorizationStore()
	tempBindings := memory.NewTempAuthKeyBindingStore(keys)
	permKey, tempKey := testAuthKey(0x91), testAuthKey(0x92)
	expiresAt := int(time.Now().Add(time.Hour).Unix())
	saveAuthKey(t, keys, permKey)
	saveAuthKeyWithExpiry(t, keys, tempKey, expiresAt)
	svc := NewService(users, auths, memory.NewCodeStore(), keys, tempBindings, "12345")

	u, err := users.Create(ctx, domain.User{Phone: "15550105203", FirstName: "PFS"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: permKey.ID, UserID: u.ID}); err != nil {
		t.Fatalf("old client signs in before the limit: %v", err)
	}
	if _, err := auths.SetDeviceLimit(ctx, u.ID, 1); err != nil {
		t.Fatalf("set device limit: %v", err)
	}
	if err := svc.UpdateAuthKeyClientInfo(ctx, tempKey.ID, domain.AuthKeyClientInfo{DeviceID: "pfs-desk"}); err != nil {
		t.Fatalf("report device id on the temp key: %v", err)
	}

	const (
		nonce     = int64(0x3344)
		sessionID = int64(0x5566)
	)
	encrypted, err := mtcrypto.EncryptBindMessage(
		bytes.NewReader(bytes.Repeat([]byte{0xAB}, 128)),
		permKey,
		int64(0x0102030405060709),
		&mtcrypto.BindAuthKeyInner{
			Nonce:         nonce,
			TempAuthKeyID: tempKey.IntID(),
			PermAuthKeyID: permKey.IntID(),
			TempSessionID: sessionID,
			ExpiresAt:     expiresAt,
		},
	)
	if err != nil {
		t.Fatalf("encrypt bind message: %v", err)
	}
	if _, err := svc.BindTempAuthKey(ctx, sessionID, domain.TempAuthKeyBinding{
		TempAuthKeyID:    tempKey.ID,
		PermAuthKeyID:    permKey.IntID(),
		Nonce:            nonce,
		ExpiresAt:        expiresAt,
		EncryptedMessage: encrypted,
	}); err != nil {
		t.Fatalf("bind temp auth key: %v", err)
	}
	got, err := auths.ListDeviceBindings(ctx, u.ID)
	if err != nil || len(got) != 1 || got[0].DeviceID != "pfs-desk" {
		t.Fatalf("bindings=%+v err=%v, want pfs-desk backfilled", got, err)
	}
}

// countingBackfillStore 记录延迟补绑被调用的次数。
type countingBackfillStore struct {
	*memory.AuthorizationStore
	calls *int
}

func (s countingBackfillStore) BackfillDeviceBinding(ctx context.Context, authKeyID [8]byte) (bool, error) {
	*s.calls++
	return s.AuthorizationStore.BackfillDeviceBinding(ctx, authKeyID)
}

// 补绑只在 device_id 本次新写入时触发：每次重连都带同一个 device_id，或拷走的会话报了
// 别的 device_id，都不再进补绑查询（热路径）。
func TestDeviceBackfillRunsOnlyWhenTheDeviceIDIsFirstStored(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	key := testAuthKey(0x83)
	saveAuthKey(t, keys, key)
	calls := 0
	svc := NewService(memory.NewUserStore(), countingBackfillStore{memory.NewAuthorizationStore(), &calls}, memory.NewCodeStore(), keys, nil, "12345")

	for _, info := range []domain.AuthKeyClientInfo{
		{DeviceModel: "PC"},
		{DeviceID: "desk-0001"},
		{DeviceID: "desk-0001"},
		{DeviceID: "desk-0002"},
	} {
		if err := svc.UpdateAuthKeyClientInfo(ctx, key.ID, info); err != nil {
			t.Fatalf("update client info %+v: %v", info, err)
		}
	}
	if calls != 1 {
		t.Fatalf("backfill calls=%d, want 1 (only the first stored device id)", calls)
	}
}

// PFS：perm key 上已有 device_id 时，temp key 带来的 device_id 不会写到 perm key 上，
// 也就不触发补绑；重放同一次绑定同样不触发。
func TestBindTempAuthKeyBackfillsOnlyWhenThePermanentKeyGainsADeviceID(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	tempBindings := memory.NewTempAuthKeyBindingStore(keys)
	permKey, tempKey := testAuthKey(0x93), testAuthKey(0x94)
	expiresAt := int(time.Now().Add(time.Hour).Unix())
	saveAuthKey(t, keys, permKey)
	saveAuthKeyWithExpiry(t, keys, tempKey, expiresAt)
	calls := 0
	svc := NewService(memory.NewUserStore(), countingBackfillStore{memory.NewAuthorizationStore(), &calls}, memory.NewCodeStore(), keys, tempBindings, "12345")
	if err := svc.UpdateAuthKeyClientInfo(ctx, permKey.ID, domain.AuthKeyClientInfo{DeviceID: "perm-desk"}); err != nil {
		t.Fatalf("report device id on the perm key: %v", err)
	}
	if err := svc.UpdateAuthKeyClientInfo(ctx, tempKey.ID, domain.AuthKeyClientInfo{DeviceID: "temp-desk"}); err != nil {
		t.Fatalf("report device id on the temp key: %v", err)
	}
	calls = 0

	const (
		nonce     = int64(0x7788)
		sessionID = int64(0x99AA)
	)
	encrypted, err := mtcrypto.EncryptBindMessage(
		bytes.NewReader(bytes.Repeat([]byte{0xAB}, 128)),
		permKey,
		int64(0x010203040506070A),
		&mtcrypto.BindAuthKeyInner{
			Nonce:         nonce,
			TempAuthKeyID: tempKey.IntID(),
			PermAuthKeyID: permKey.IntID(),
			TempSessionID: sessionID,
			ExpiresAt:     expiresAt,
		},
	)
	if err != nil {
		t.Fatalf("encrypt bind message: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := svc.BindTempAuthKey(ctx, sessionID, domain.TempAuthKeyBinding{
			TempAuthKeyID:    tempKey.ID,
			PermAuthKeyID:    permKey.IntID(),
			Nonce:            nonce,
			ExpiresAt:        expiresAt,
			EncryptedMessage: encrypted,
		}); err != nil {
			t.Fatalf("bind temp auth key (attempt %d): %v", i+1, err)
		}
	}
	if calls != 0 {
		t.Fatalf("backfill calls=%d, want 0 when the perm key already had a device id", calls)
	}
	perm, _, err := keys.Revalidate(ctx, permKey.ID)
	if err != nil || perm.DeviceID != "perm-desk" {
		t.Fatalf("perm device_id=%q err=%v, want perm-desk kept", perm.DeviceID, err)
	}
}
