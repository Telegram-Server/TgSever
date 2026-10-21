package push

import (
	"testing"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// noVoIPSender 是证书不覆盖 VoIP topic 的发送器：普通推送照发，VoIP 不可用。
type noVoIPSender struct{ *recordingSender }

func (noVoIPSender) VoIPEnabled() bool { return false }

func TestIncomingCallIsNotQueuedWhenSenderCannotSendVoIP(t *testing.T) {
	f := newServiceFixture(t, Config{})
	f.svc = NewService(Config{}, Deps{
		Devices: f.devices, Authorizations: f.auths, Presence: f.presence,
		Sender: noVoIPSender{f.sender}, Strings: testLangPack(),
	}, zaptest.NewLogger(t))
	f.register([8]byte{2}, bobID, domain.PushTokenVoIP, "voip-offline", testSecret(), presenceNone)
	f.register([8]byte{3}, bobID, domain.PushTokenAPNs, "apns-offline", nil, presenceNone)
	f.start()
	enc := &layerEncoder{layers: map[[8]byte]int{{2}: 228}}
	if f.svc.IncomingCall(incomingCallFromAlice(enc.encode)) {
		t.Fatal("IncomingCall queued, want rejected while voip is disabled")
	}
	if !f.svc.NewMessage(textFromAlice("still works")) {
		t.Fatal("NewMessage rejected, want regular push to stay on")
	}
	f.stop()
	if got := f.sender.tokens(); len(got) != 1 || got[0] != "apns-offline" {
		t.Fatalf("pushed tokens = %v, want only the regular push", got)
	}
}
