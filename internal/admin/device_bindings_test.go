package admin

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// 后台读取账号设备绑定名单（#113）：按绑定先后返回每台设备的型号、平台、客户端版本、绑定时间与
// 最近使用时间；没有绑定的账号返回空列表（JSON 里是 []，不是 null）。

func TestDeviceBindingsListsBoundDevicesInBindingOrder(t *testing.T) {
	f := newDeviceLimitFixture(t)
	keys := f.keys
	ctx := context.Background()
	u := f.user(t, domain.User{Phone: "15550105201", FirstName: "Staff"})
	if _, err := f.auths.SetDeviceLimit(ctx, u.ID, 3); err != nil {
		t.Fatalf("set limit: %v", err)
	}
	for i, d := range []struct{ id, model, platform, version string }{
		{"laptop-1", "ThinkPad X1", "Windows", "5.2.1"},
		{"phone-1", "Pixel 9", "Android", "11.2"},
	} {
		key := [8]byte{0x13, byte(i + 1)}
		if err := keys.Save(ctx, store.AuthKeyData{ID: key, ServerSalt: 1}); err != nil {
			t.Fatalf("save key: %v", err)
		}
		if err := keys.UpdateClientInfo(ctx, key, store.AuthKeyClientInfo{DeviceID: d.id}); err != nil {
			t.Fatalf("persist device id: %v", err)
		}
		if err := f.auths.Bind(ctx, domain.Authorization{
			AuthKeyID: key, UserID: u.ID, DeviceModel: d.model, Platform: d.platform, AppVersion: d.version,
		}); err != nil {
			t.Fatalf("bind %s: %v", d.id, err)
		}
	}

	got, err := f.svc.DeviceBindings(ctx, u.ID)
	if err != nil {
		t.Fatalf("device bindings: %v", err)
	}
	if got.UserID != u.ID || len(got.Bindings) != 2 {
		t.Fatalf("bindings = %+v, want two for user %d", got, u.ID)
	}
	first, second := got.Bindings[0], got.Bindings[1]
	if first.DeviceID != "laptop-1" || first.DeviceModel != "ThinkPad X1" || first.Platform != "Windows" || first.AppVersion != "5.2.1" {
		t.Fatalf("first binding = %+v, want laptop-1 ThinkPad X1 / Windows / 5.2.1", first)
	}
	if second.DeviceID != "phone-1" || second.DeviceModel != "Pixel 9" || second.Platform != "Android" || second.AppVersion != "11.2" {
		t.Fatalf("second binding = %+v, want phone-1 Pixel 9 / Android / 11.2", second)
	}
	for _, b := range got.Bindings {
		if b.BoundAt.IsZero() || b.LastUsedAt.Before(b.BoundAt) {
			t.Fatalf("binding times = %+v, want bound and last-used set", b)
		}
	}
}

func TestDeviceBindingsReturnsAnEmptyListForAnUnboundAccount(t *testing.T) {
	f := newDeviceLimitFixture(t)
	u := f.user(t, domain.User{Phone: "15550105202", FirstName: "Free"})

	got, err := f.svc.DeviceBindings(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("device bindings: %v", err)
	}
	if got.UserID != u.ID || got.Bindings == nil || len(got.Bindings) != 0 {
		t.Fatalf("bindings = %#v, want an empty, non-nil list", got)
	}
}
