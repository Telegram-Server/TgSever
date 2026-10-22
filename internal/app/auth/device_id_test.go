package auth

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	mtcrypto "github.com/iamxvbaba/td/crypto"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// device_id 随"按 auth key 持久化客户端信息"写入，之后从 auth key 读得到（#111）。
func TestUpdateAuthKeyClientInfoPersistsDeviceID(t *testing.T) {
	ctx := context.Background()
	keys := memory.NewAuthKeyStore()
	key := testAuthKey(0x61)
	saveAuthKey(t, keys, key)
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), keys, nil, "12345")

	if err := svc.UpdateAuthKeyClientInfo(ctx, key.ID, domain.AuthKeyClientInfo{DeviceModel: "PC", DeviceID: "desk-0001"}); err != nil {
		t.Fatalf("update client info: %v", err)
	}
	if err := svc.UpdateAuthKeyClientInfo(ctx, key.ID, domain.AuthKeyClientInfo{DeviceModel: "PC 2"}); err != nil {
		t.Fatalf("update client info without device id: %v", err)
	}
	info, found, err := svc.AuthKeyClientInfo(ctx, key.ID)
	if err != nil || !found || info.DeviceID != "desk-0001" {
		t.Fatalf("client info device_id=%q found=%v err=%v, want desk-0001", info.DeviceID, found, err)
	}
}

// PFS：temp key 先上报 device_id 再绑定到 perm key 时，值要落到 perm key 上；perm key
// 已有值而 temp key 没上报时，绑定不清掉它。
func TestBindTempAuthKeyCarriesDeviceIDToPermanentKey(t *testing.T) {
	for _, tc := range []struct {
		name               string
		tempID, permBefore string
		wantPerm           string
	}{
		{name: "temp reported", tempID: "desk-temp", wantPerm: "desk-temp"},
		{name: "perm keeps its device id", tempID: "desk-new", permBefore: "desk-old", wantPerm: "desk-old"},
		{name: "temp silent", permBefore: "desk-old", wantPerm: "desk-old"},
		{name: "neither reported", wantPerm: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			keys := memory.NewAuthKeyStore()
			tempBindings := memory.NewTempAuthKeyBindingStore(keys)
			permKey := testAuthKey(0x71)
			tempKey := testAuthKey(0x72)
			expiresAt := int(time.Now().Add(time.Hour).Unix())
			saveAuthKey(t, keys, permKey)
			saveAuthKeyWithExpiry(t, keys, tempKey, expiresAt)
			svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(), keys, tempBindings, "12345")
			if tc.permBefore != "" {
				if err := svc.UpdateAuthKeyClientInfo(ctx, permKey.ID, domain.AuthKeyClientInfo{DeviceID: tc.permBefore}); err != nil {
					t.Fatalf("seed perm device id: %v", err)
				}
			}
			if tc.tempID != "" {
				if err := svc.UpdateAuthKeyClientInfo(ctx, tempKey.ID, domain.AuthKeyClientInfo{DeviceID: tc.tempID}); err != nil {
					t.Fatalf("seed temp device id: %v", err)
				}
			}

			if _, err := bindTempAuthKeyForTest(t, svc, tempKey, permKey, expiresAt); err != nil {
				t.Fatalf("bind temp auth key: %v", err)
			}
			perm, found, err := keys.Revalidate(ctx, permKey.ID)
			if err != nil || !found || perm.DeviceID != tc.wantPerm {
				t.Fatalf("perm device_id=%q found=%v err=%v, want %q", perm.DeviceID, found, err, tc.wantPerm)
			}
		})
	}
}

// bindTempAuthKeyForTest 用 permKey 签一条 bindTempAuthKey 消息，把 tempKey 绑定到 permKey。
func bindTempAuthKeyForTest(t *testing.T, svc *Service, tempKey, permKey mtcrypto.AuthKey, expiresAt int) (domain.TempAuthKeyBindingResult, error) {
	t.Helper()
	const (
		nonce     = int64(0x2233)
		sessionID = int64(0x4455)
	)
	encrypted, err := mtcrypto.EncryptBindMessage(
		bytes.NewReader(bytes.Repeat([]byte{0xAB}, 128)),
		permKey,
		int64(0x0102030405060708),
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
	return svc.BindTempAuthKey(context.Background(), sessionID, domain.TempAuthKeyBinding{
		TempAuthKeyID:    tempKey.ID,
		PermAuthKeyID:    permKey.IntID(),
		Nonce:            nonce,
		ExpiresAt:        expiresAt,
		EncryptedMessage: encrypted,
	})
}

// failingDeviceIDKeys 让写入 failOn 这把 key 的客户端信息失败。
type failingDeviceIDKeys struct {
	store.AuthKeyStore
	failOn [8]byte
}

func (k failingDeviceIDKeys) UpdateClientInfo(ctx context.Context, id [8]byte, info store.AuthKeyClientInfo) (bool, error) {
	if id == k.failOn {
		return false, errors.New("auth key write failed")
	}
	return k.AuthKeyStore.UpdateClientInfo(ctx, id, info)
}

// 把 device_id 带到 perm key 与常规的客户端信息持久化一样是尽力而为：写库失败只交给 observer
// 记录，bindTempAuthKey 照常成功。
func TestBindTempAuthKeySucceedsWhenCarryingTheDeviceIDFails(t *testing.T) {
	ctx := context.Background()
	memKeys := memory.NewAuthKeyStore()
	tempBindings := memory.NewTempAuthKeyBindingStore(memKeys)
	permKey := testAuthKey(0x73)
	tempKey := testAuthKey(0x74)
	expiresAt := int(time.Now().Add(time.Hour).Unix())
	saveAuthKey(t, memKeys, permKey)
	saveAuthKeyWithExpiry(t, memKeys, tempKey, expiresAt)
	var observed [][8]byte
	svc := NewService(memory.NewUserStore(), memory.NewAuthorizationStore(), memory.NewCodeStore(),
		failingDeviceIDKeys{AuthKeyStore: memKeys, failOn: permKey.ID}, tempBindings, "12345",
		WithDeviceIDCarryFailureObserver(func(_ context.Context, permAuthKeyID [8]byte, err error) {
			if err == nil {
				t.Errorf("observer got a nil error")
			}
			observed = append(observed, permAuthKeyID)
		}))
	if err := svc.UpdateAuthKeyClientInfo(ctx, tempKey.ID, domain.AuthKeyClientInfo{DeviceID: "desk-temp"}); err != nil {
		t.Fatalf("seed temp device id: %v", err)
	}
	if _, err := bindTempAuthKeyForTest(t, svc, tempKey, permKey, expiresAt); err != nil {
		t.Fatalf("bind temp auth key err=%v, want success when carrying the device id fails", err)
	}
	if len(observed) != 1 || observed[0] != permKey.ID {
		t.Fatalf("carry failures observed=%x, want one for the perm key %x", observed, permKey.ID)
	}
}
