package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// 桌面会话设的铃声进 other_sound 并原样读回、随 updateNotifySettings 推出；安卓会话
// 随后设的静音只进 android_sound，不覆盖桌面的铃声。
func TestUpdateNotifySettingsSoundLandsInTheCallersPlatformSlot(t *testing.T) {
	r, sessions := notifyRouter(t)
	const viewer = int64(1000000002)
	peerInput := &tg.InputNotifyPeer{Peer: &tg.InputPeerUser{UserID: 556}}
	desktop := WithClientInfo(WithUserID(context.Background(), viewer), ClientInfo{Type: ClientTypeTDesktop, AppVersion: "6.2.4"})
	android := WithClientInfo(WithUserID(context.Background(), viewer), ClientInfo{Type: ClientTypeAndroid, AppVersion: "12.9.0 (69669)"})

	in := tg.InputPeerNotifySettings{}
	in.SetSound(&tg.NotificationSoundRingtone{ID: 9001})
	if ok, err := r.onAccountUpdateNotifySettings(desktop, &tg.AccountUpdateNotifySettingsRequest{Peer: peerInput, Settings: in}); err != nil || !ok {
		t.Fatalf("desktop update = ok %v err %v", ok, err)
	}
	updates, ok := sessions.snapshot().message.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed = %#v, want one update", sessions.snapshot().message)
	}
	pushed := updates.Updates[0].(*tg.UpdateNotifySettings).NotifySettings
	if s, _ := pushed.GetOtherSound(); !isRingtone(s, 9001) {
		t.Fatalf("pushed other_sound = %#v, want ringtone 9001", s)
	}

	in = tg.InputPeerNotifySettings{}
	in.SetSound(&tg.NotificationSoundNone{})
	if ok, err := r.onAccountUpdateNotifySettings(android, &tg.AccountUpdateNotifySettingsRequest{Peer: peerInput, Settings: in}); err != nil || !ok {
		t.Fatalf("android update = ok %v err %v", ok, err)
	}

	got, err := r.onAccountGetNotifySettings(desktop, peerInput)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := got.GetOtherSound(); !isRingtone(s, 9001) {
		t.Fatalf("other_sound = %#v, want ringtone 9001", s)
	}
	if s, _ := got.GetAndroidSound(); s == nil || s.TypeID() != tg.NotificationSoundNoneTypeID {
		t.Fatalf("android_sound = %#v, want none", s)
	}
	if s, _ := got.GetIosSound(); s == nil || s.TypeID() != tg.NotificationSoundDefaultTypeID {
		t.Fatalf("ios_sound = %#v, want default", s)
	}
}

func isRingtone(s tg.NotificationSoundClass, id int64) bool {
	r, ok := s.(*tg.NotificationSoundRingtone)
	return ok && r.ID == id
}
