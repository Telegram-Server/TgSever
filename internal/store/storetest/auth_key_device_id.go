package storetest

import (
	"context"
	"crypto/rand"
	"testing"

	"telesrv/internal/store"
)

// AuthKeyDeviceIDContract 是 auth key 上 device_id 持久化的存储契约（#111）：
// UpdateClientInfo 写入的 device_id 能经 Get/Revalidate 读回；空值不覆盖已有值，
// 只更新其它元数据也不会清掉它；新的非空值覆盖旧值；协议层重复 Save 不清掉它。
// reopen 返回同一份底层数据上的另一个 store 实例（postgres 借此模拟服务端重启），
// 写入的值在那里同样可读。
func AuthKeyDeviceIDContract(t *testing.T, newStore func(t *testing.T) store.AuthKeyStore, reopen func(t *testing.T) store.AuthKeyStore) {
	t.Helper()
	ctx := context.Background()

	t.Run("new key reports no device id", func(t *testing.T) {
		keys := newStore(t)
		id := newDeviceIDTestKey(t, keys)
		assertAuthKeyDeviceID(t, keys, id, "")
	})

	t.Run("update persists device id", func(t *testing.T) {
		keys := newStore(t)
		id := newDeviceIDTestKey(t, keys)
		if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{DeviceModel: "PC", DeviceID: "desk-0001"}); err != nil {
			t.Fatalf("update client info: %v", err)
		}
		assertAuthKeyDeviceID(t, keys, id, "desk-0001")
		assertAuthKeyDeviceID(t, reopen(t), id, "desk-0001")
	})

	t.Run("empty device id keeps the stored value", func(t *testing.T) {
		keys := newStore(t)
		id := newDeviceIDTestKey(t, keys)
		if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{DeviceID: "desk-0002"}); err != nil {
			t.Fatalf("update client info: %v", err)
		}
		if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{DeviceModel: "Laptop", AppVersion: "6.0"}); err != nil {
			t.Fatalf("update other metadata: %v", err)
		}
		assertAuthKeyDeviceID(t, keys, id, "desk-0002")
		data, _, err := keys.Revalidate(ctx, id)
		if err != nil || data.DeviceModel != "Laptop" {
			t.Fatalf("device model = %q err=%v, want Laptop", data.DeviceModel, err)
		}
	})

	t.Run("non-empty device id replaces the stored value", func(t *testing.T) {
		keys := newStore(t)
		id := newDeviceIDTestKey(t, keys)
		for _, deviceID := range []string{"desk-0003", "desk-0004"} {
			if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{DeviceID: deviceID}); err != nil {
				t.Fatalf("update device id %q: %v", deviceID, err)
			}
		}
		assertAuthKeyDeviceID(t, keys, id, "desk-0004")
	})

	t.Run("protocol re-save keeps device id", func(t *testing.T) {
		keys := newStore(t)
		id := newDeviceIDTestKey(t, keys)
		if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{DeviceID: "desk-0005"}); err != nil {
			t.Fatalf("update client info: %v", err)
		}
		if err := keys.Save(ctx, store.AuthKeyData{ID: id, ServerSalt: 2}); err != nil {
			t.Fatalf("re-save auth key: %v", err)
		}
		assertAuthKeyDeviceID(t, keys, id, "desk-0005")
	})
}

func newDeviceIDTestKey(t *testing.T, keys store.AuthKeyStore) [8]byte {
	t.Helper()
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatalf("random auth key id: %v", err)
	}
	if err := keys.Save(context.Background(), store.AuthKeyData{ID: id, ServerSalt: 1}); err != nil {
		t.Fatalf("save auth key %x: %v", id, err)
	}
	t.Cleanup(func() { _ = keys.Delete(context.Background(), id) })
	return id
}

func assertAuthKeyDeviceID(t *testing.T, keys store.AuthKeyStore, id [8]byte, want string) {
	t.Helper()
	ctx := context.Background()
	got, found, err := keys.Revalidate(ctx, id)
	if err != nil || !found || got.DeviceID != want {
		t.Fatalf("revalidate %x device_id=%q found=%v err=%v, want %q", id, got.DeviceID, found, err, want)
	}
	got, found, err = keys.Get(ctx, id)
	if err != nil || !found || got.DeviceID != want {
		t.Fatalf("get %x device_id=%q found=%v err=%v, want %q", id, got.DeviceID, found, err, want)
	}
}
