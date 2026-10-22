package auth

import (
	"bytes"
	"context"
	"testing"
	"time"

	mtcrypto "github.com/iamxvbaba/td/crypto"

	"telesrv/internal/domain"
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
			if _, err := svc.BindTempAuthKey(ctx, sessionID, domain.TempAuthKeyBinding{
				TempAuthKeyID:    tempKey.ID,
				PermAuthKeyID:    permKey.IntID(),
				Nonce:            nonce,
				ExpiresAt:        expiresAt,
				EncryptedMessage: encrypted,
			}); err != nil {
				t.Fatalf("bind temp auth key: %v", err)
			}
			perm, found, err := keys.Revalidate(ctx, permKey.ID)
			if err != nil || !found || perm.DeviceID != tc.wantPerm {
				t.Fatalf("perm device_id=%q found=%v err=%v, want %q", perm.DeviceID, found, err, tc.wantPerm)
			}
		})
	}
}
