package push

import (
	"context"
	"slices"
	"testing"

	"telesrv/internal/domain"
)

// disable 把 authKeyID 下 userID 的登记设为后台关闭推送（push.set_device_enabled 的效果）。
func (f *serviceFixture) disable(userID int64, authKeyID [8]byte) {
	f.t.Helper()
	if n, err := f.devices.SetPushDevicesEnabled(context.Background(), userID, authKeyID, false); err != nil || n == 0 {
		f.t.Fatalf("disable push of %v: updated=%d err=%v", authKeyID, n, err)
	}
}

// 后台关闭推送的设备什么推送都不收：私聊新消息、群消息、来电、已读同步、删除同步；
// 同一个人开着推送的另一台设备照常收。
func TestDisabledDeviceReceivesNoPushOfAnyKind(t *testing.T) {
	cases := []struct {
		name    string
		voip    bool
		trigger func(f *channelFixture) bool
	}{
		{"private message", false, func(f *channelFixture) bool { return f.svc.NewMessage(textFromAlice("hello")) }},
		{"group message", false, func(f *channelFixture) bool {
			f.members.members[groupID] = []int64{aliceID, bobID}
			return f.svc.ChannelMessage(groupTextFromAlice("hi team"))
		}},
		{"incoming call", true, func(f *channelFixture) bool {
			enc := &layerEncoder{layers: map[[8]byte]int{{1}: 230, {2}: 230}}
			return f.svc.IncomingCall(incomingCallFromAlice(enc.encode))
		}},
		{"read history", false, func(f *channelFixture) bool {
			return f.svc.ReadHistory(ReadHistory{UserID: bobID, Chat: Chat{Kind: ChatPrivate, ID: aliceID}, MaxID: 77})
		}},
		{"message deleted", false, func(f *channelFixture) bool {
			return f.svc.DeleteMessages(DeletedMessages{UserID: bobID, Chat: Chat{Kind: ChatPrivate, ID: aliceID}, MessageIDs: []int{77}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newChannelFixture(t, Config{})
			tokenType := domain.PushTokenAPNs
			if tc.voip {
				tokenType = domain.PushTokenVoIP
			}
			f.register([8]byte{1}, bobID, tokenType, "tok-enabled", testSecret(), presenceNone)
			f.register([8]byte{2}, bobID, tokenType, "tok-disabled", testSecret(), presenceNone)
			f.disable(bobID, [8]byte{2})
			f.start()
			if !tc.trigger(f) {
				t.Fatal("push rejected, want queued")
			}
			f.stop()
			if got := f.sender.tokens(); !slices.Equal(got, []string{"tok-enabled"}) {
				t.Fatalf("pushed tokens = %v, want only the device with push enabled", got)
			}
		})
	}
}
