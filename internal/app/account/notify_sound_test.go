package account

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 一台设备只写自己平台的声音槽：桌面设铃声、安卓设静音互不覆盖；桌面再提交一份
// 不带声音的设置只清掉自己的槽，安卓的槽保留。
func TestUpdateNotifySettingsWritesOnlyTheCallersSoundSlot(t *testing.T) {
	ctx := context.Background()
	st := memory.NewPasswordStore()
	svc := NewService(st, WithNotifySettings(st))
	const owner = int64(42)
	scope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 7}}
	ringtone := domain.NotificationSound{Kind: domain.NotificationSoundRingtone, RingtoneID: 9001}
	none := domain.NotificationSound{Kind: domain.NotificationSoundNone}
	mute := 100

	desktop := domain.PeerNotifySettings{MuteUntil: &mute}
	desktop.Sound = desktop.Sound.With(domain.NotifySoundSlotOther, &ringtone)
	if _, err := svc.UpdateNotifySettings(ctx, owner, scope, domain.NotifySoundSlotOther, desktop); err != nil {
		t.Fatal(err)
	}
	android := domain.PeerNotifySettings{MuteUntil: &mute}
	android.Sound = android.Sound.With(domain.NotifySoundSlotAndroid, &none)
	if _, err := svc.UpdateNotifySettings(ctx, owner, scope, domain.NotifySoundSlotAndroid, android); err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetNotifySettings(ctx, owner, scope)
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Sound.Get(domain.NotifySoundSlotOther); s == nil || *s != ringtone {
		t.Fatalf("other slot = %+v, want %+v", s, ringtone)
	}
	if s := got.Sound.Get(domain.NotifySoundSlotAndroid); s == nil || *s != none {
		t.Fatalf("android slot = %+v, want %+v", s, none)
	}
	if s := got.Sound.Get(domain.NotifySoundSlotIOS); s != nil {
		t.Fatalf("ios slot = %+v, want unset", s)
	}

	if _, err := svc.UpdateNotifySettings(ctx, owner, scope, domain.NotifySoundSlotOther, domain.PeerNotifySettings{MuteUntil: &mute}); err != nil {
		t.Fatal(err)
	}
	got, err = svc.GetNotifySettings(ctx, owner, scope)
	if err != nil {
		t.Fatal(err)
	}
	if s := got.Sound.Get(domain.NotifySoundSlotOther); s != nil {
		t.Fatalf("other slot after clearing = %+v, want unset", s)
	}
	if s := got.Sound.Get(domain.NotifySoundSlotAndroid); s == nil || *s != none {
		t.Fatalf("android slot after desktop clear = %+v, want %+v", s, none)
	}
}
