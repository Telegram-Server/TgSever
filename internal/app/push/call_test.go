package push

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
)

// decryptVoIP 按 iOS AppDelegate（PushKit）的步骤解出 VoIP payload：外层只有 p，
// 解密后 JSON 的 updates 是 base64url 编码的 TL 字节。
func decryptVoIP(t *testing.T, secret, payload []byte) (map[string]any, []byte) {
	t.Helper()
	outer := decodeJSONObject(t, payload)
	if len(outer) != 1 {
		t.Fatalf("outer voip payload = %s, want only p", payload)
	}
	p, _ := outer["p"].(string)
	plain, _, err := nseDecrypt(secret, p)
	if err != nil {
		t.Fatalf("decrypt voip payload: %v", err)
	}
	inner := decodeJSONObject(t, plain)
	encoded, ok := inner["updates"].(string)
	if !ok {
		t.Fatalf("decrypted voip payload = %s, want an updates string", plain)
	}
	updates, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("updates is not unpadded base64url: %v", err)
	}
	return inner, updates
}

func TestBuildVoIPPayloadCarriesUpdatesEncrypted(t *testing.T) {
	secret := testSecret()
	updates := []byte{0xae, 0x0b, 0x4e, 0x74, 1, 2, 3, 0xff}
	payload, err := buildVoIPPayload(updates, secret)
	if err != nil {
		t.Fatal(err)
	}
	inner, got := decryptVoIP(t, secret, payload)
	if !bytes.Equal(got, updates) {
		t.Fatalf("updates = %x, want %x", got, updates)
	}
	// 只带 updates：from_id/group_call_id/msg_id 会把客户端带进群通话分支。
	if len(inner) != 1 {
		t.Fatalf("decrypted voip payload = %v, want only updates", inner)
	}
	if _, err := buildVoIPPayload(updates, nil); err == nil {
		t.Fatal("buildVoIPPayload without secret succeeded; PushKit requires p")
	}
}

// layerEncoder 按授权记下的 layer 序列化：输出 "layer:<n>"，便于断言每台设备用的是自己的 layer。
type layerEncoder struct {
	mu     sync.Mutex
	layers map[[8]byte]int
	calls  [][8]byte
}

func (e *layerEncoder) encode(_ context.Context, authKeyID [8]byte) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, authKeyID)
	layer, ok := e.layers[authKeyID]
	if !ok {
		return nil, errors.New("no layer")
	}
	return []byte{byte(layer)}, nil
}

func incomingCallFromAlice(enc CallUpdatesEncoder) IncomingCall {
	return IncomingCall{CallID: 501, CallerID: aliceID, CalleeID: bobID, Updates: enc}
}

func TestIncomingCallPushesOnlyVoIPDevicesNotInForeground(t *testing.T) {
	f := newServiceFixture(t, Config{})
	secret := testSecret()
	enc := &layerEncoder{layers: map[[8]byte]int{{2}: 228, {3}: 230, {4}: 229, {6}: 227}}
	f.register([8]byte{1}, bobID, domain.PushTokenVoIP, "voip-foreground", secret, presenceForeground)
	f.register([8]byte{2}, bobID, domain.PushTokenVoIP, "voip-background", secret, presenceBackground)
	f.register([8]byte{3}, bobID, domain.PushTokenVoIP, "voip-offline", secret, presenceNone)
	// 普通登记不收来电推送：来电只走 PushKit。
	f.register([8]byte{4}, bobID, domain.PushTokenAPNs, "apns-offline", secret, presenceNone)
	// PushKit 只认加密 payload：没有 secret 的 VoIP 登记不推。
	f.register([8]byte{5}, bobID, domain.PushTokenVoIP, "voip-no-secret", nil, presenceNone)
	// 主叫自己的设备不推。
	f.register([8]byte{6}, aliceID, domain.PushTokenVoIP, "voip-alice", secret, presenceNone)
	f.start()

	if !f.svc.IncomingCall(incomingCallFromAlice(enc.encode)) {
		t.Fatal("IncomingCall rejected, want queued")
	}
	f.stop()

	got := f.sender.tokens()
	want := []string{"voip-background", "voip-offline"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pushed tokens = %v, want %v", got, want)
	}
	for _, n := range f.sender.sent {
		if n.Type != PushTypeVoIP || n.Priority != PriorityHigh || !n.ExpireImmediately {
			t.Fatalf("notification = %+v, want voip, priority 10, apns-expiration 0", n)
		}
		_, updates := decryptVoIP(t, secret, n.Payload)
		wantLayer := map[string]int{"voip-background": 228, "voip-offline": 230}[n.Token]
		if len(updates) != 1 || int(updates[0]) != wantLayer {
			t.Fatalf("%s: updates = %v, want encoded for layer %d", n.Token, updates, wantLayer)
		}
	}
}

func TestIncomingCallSkipsDeviceWhoseUpdatesFailToEncode(t *testing.T) {
	f := newServiceFixture(t, Config{})
	enc := &layerEncoder{layers: map[[8]byte]int{{3}: 230}}
	f.register([8]byte{2}, bobID, domain.PushTokenVoIP, "voip-broken", testSecret(), presenceNone)
	f.register([8]byte{3}, bobID, domain.PushTokenVoIP, "voip-ok", testSecret(), presenceNone)
	f.start()
	f.svc.IncomingCall(incomingCallFromAlice(enc.encode))
	f.stop()
	if got := f.sender.tokens(); len(got) != 1 || got[0] != "voip-ok" {
		t.Fatalf("pushed tokens = %v, want only voip-ok", got)
	}
}

func TestIncomingCallRejectsIncompleteEvent(t *testing.T) {
	f := newServiceFixture(t, Config{})
	enc := &layerEncoder{}
	if f.svc.IncomingCall(IncomingCall{CallerID: aliceID, CalleeID: bobID}) {
		t.Fatal("IncomingCall without encoder accepted")
	}
	if f.svc.IncomingCall(IncomingCall{CallerID: bobID, CalleeID: bobID, Updates: enc.encode}) {
		t.Fatal("IncomingCall to the caller accepted")
	}
	svc := NewService(Config{}, Deps{}, nil)
	if svc.IncomingCall(incomingCallFromAlice(enc.encode)) {
		t.Fatal("IncomingCall accepted with no sender, want disabled")
	}
}

func TestIncomingCallSkipsDeviceWhereCallerIsAlsoLoggedIn(t *testing.T) {
	f := newServiceFixture(t, Config{})
	ctx := context.Background()
	enc := &layerEncoder{layers: map[[8]byte]int{{2}: 230, {3}: 230}}
	// 设备 2：同一台 iPhone 上也登录着主叫 Alice，不响铃；设备 3：别的手机，照常推。
	for _, d := range []struct {
		key    [8]byte
		token  string
		others []int64
	}{
		{[8]byte{2}, "voip-shared-phone", []int64{aliceID}},
		{[8]byte{3}, "voip-other-phone", []int64{1000099}},
	} {
		if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: d.key, UserID: bobID, Hash: int64(d.key[0])}); err != nil {
			t.Fatal(err)
		}
		if err := f.devices.RegisterPushDevice(ctx, domain.PushDevice{AuthKeyID: d.key, UserID: bobID, TokenType: domain.PushTokenVoIP, Token: d.token, Secret: testSecret(), OtherUIDs: d.others}); err != nil {
			t.Fatal(err)
		}
	}
	f.start()
	f.svc.IncomingCall(incomingCallFromAlice(enc.encode))
	f.stop()
	if got := f.sender.tokens(); len(got) != 1 || got[0] != "voip-other-phone" {
		t.Fatalf("pushed tokens = %v, want only voip-other-phone", got)
	}
}
