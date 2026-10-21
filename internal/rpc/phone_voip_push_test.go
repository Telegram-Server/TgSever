package rpc

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"sync"
	"testing"

	"github.com/gotd/ige"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/crypto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	appauth "telesrv/internal/app/auth"
	appphone "telesrv/internal/app/phone"
	"telesrv/internal/app/push"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type callPushNotifierFunc func(push.IncomingCall) bool

func (f callPushNotifierFunc) IncomingCall(c push.IncomingCall) bool { return f(c) }

// decodeVoIPCallUpdates 按指定 layer 反序列化 VoIP payload 里的 updates，
// 取出 iOS extractIncomingCallUpdate 需要的东西：constructor updates、phoneCallRequested、users。
func decodeVoIPCallUpdates(t *testing.T, profile tlprofile.Profile, raw []byte) (*tg.PhoneCallRequested, *tg.Updates) {
	t.Helper()
	obj, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: raw}, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("decode updates at layer %d: %v", profile, err)
	}
	updates, ok := obj.(*tg.Updates)
	if !ok {
		t.Fatalf("decoded %T, want constructor updates (iOS rejects updatesCombined/updateShort)", obj)
	}
	for _, u := range updates.Updates {
		if pc, ok := u.(*tg.UpdatePhoneCall); ok {
			if requested, ok := pc.PhoneCall.(*tg.PhoneCallRequested); ok {
				return requested, updates
			}
		}
	}
	t.Fatalf("updates = %+v, want updatePhoneCall(phoneCallRequested)", updates.Updates)
	return nil, nil
}

func voipUpdatesUser(updates *tg.Updates, userID int64) (*tg.User, bool) {
	for _, u := range updates.Users {
		if user, ok := u.(*tg.User); ok && user.ID == userID {
			return user, true
		}
	}
	return nil, false
}

// 函数层：来电 Updates 按设备授权记下的 layer 序列化，同一 layer 能还原出 phoneCallRequested 与主叫。
func TestVoIPCallUpdatesEncodeForDeviceLayer(t *testing.T) {
	f := newPhoneFixture(t, stubPrivacy{})
	var calls []push.IncomingCall
	f.router.SetCallPushNotifier(callPushNotifierFunc(func(c push.IncomingCall) bool { calls = append(calls, c); return true }))
	_, gaHash, _ := phoneTestKeys()
	if _, err := f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID:   &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash},
		RandomID: 99,
		GAHash:   gaHash,
		Video:    true,
		Protocol: phoneTestProtocol(),
	}); err != nil {
		t.Fatalf("requestCall: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("incoming call pushes = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.CallerID != f.caller.ID || call.CalleeID != f.callee.ID || call.CallID == 0 || call.Updates == nil {
		t.Fatalf("incoming call = %+v", call)
	}

	device228, deviceUnknown := [8]byte{0x99, 0x28}, [8]byte{0x99, 0x00}
	f.router.setAuthKeyLayerDefaults(228, device228)
	for _, tc := range []struct {
		device  [8]byte
		profile tlprofile.Profile
	}{
		{device228, tlprofile.Profile228},
		// 没有记下 layer 的授权按服务端规范 layer 序列化。
		{deviceUnknown, tlprofile.ProfileCanonical},
	} {
		raw, err := call.Updates(context.Background(), tc.device)
		if err != nil {
			t.Fatalf("encode for %x: %v", tc.device, err)
		}
		requested, updates := decodeVoIPCallUpdates(t, tc.profile, raw)
		if requested.ID != call.CallID || requested.AdminID != f.caller.ID || requested.ParticipantID != f.callee.ID || !requested.Video {
			t.Fatalf("phoneCallRequested = %+v", requested)
		}
		caller, ok := voipUpdatesUser(updates, f.caller.ID)
		if !ok || caller.FirstName != "Caller" || caller.AccessHash == 0 {
			t.Fatalf("updates users = %+v, want the caller (admin_id) with name and access hash", updates.Users)
		}
	}
}

func TestRequestCallWithoutCallPushStillSucceeds(t *testing.T) {
	f := newPhoneFixture(t, stubPrivacy{})
	_, gaHash, _ := phoneTestKeys()
	if _, err := f.router.onPhoneRequestCall(f.callerCtx(), &tg.PhoneRequestCallRequest{
		UserID:   &tg.InputUser{UserID: f.callee.ID, AccessHash: f.callee.AccessHash},
		RandomID: 7,
		GAHash:   gaHash,
		Protocol: phoneTestProtocol(),
	}); err != nil {
		t.Fatalf("requestCall: %v", err)
	}
}

// voipDecrypt 按 iOS AppDelegate 解出 VoIP payload 的 updates 字节（MTProto 2.0 服务端方向，x=8）。
func voipDecrypt(t *testing.T, secret []byte, payload []byte) []byte {
	t.Helper()
	var outer map[string]string
	if err := json.Unmarshal(payload, &outer); err != nil || len(outer) != 1 || outer["p"] == "" {
		t.Fatalf("voip payload = %s, want only p (err %v)", payload, err)
	}
	data, err := base64.RawURLEncoding.DecodeString(outer["p"])
	if err != nil || len(data) < 24 {
		t.Fatalf("p is not base64url key_id||msg_key||data: %v", err)
	}
	var key crypto.Key
	copy(key[:], secret)
	id := key.ID()
	if !bytes.Equal(data[:8], id[:]) {
		t.Fatal("key id mismatch")
	}
	var msgKey bin.Int128
	copy(msgKey[:], data[8:24])
	aesKey, aesIV := crypto.Keys(key, msgKey, crypto.Server)
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		t.Fatal(err)
	}
	plain := make([]byte, len(data)-24)
	ige.DecryptBlocks(block, aesIV[:], plain, data[24:])
	if crypto.MessageKey(key, plain, crypto.Server) != msgKey {
		t.Fatal("msg key mismatch")
	}
	n := int(binary.LittleEndian.Uint32(plain[:4]))
	var inner map[string]string
	if err := json.Unmarshal(plain[4:4+n], &inner); err != nil || len(inner) != 1 {
		t.Fatalf("decrypted voip payload = %s, want only updates (err %v)", plain[4:4+n], err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(inner["updates"])
	if err != nil {
		t.Fatalf("updates is not base64url: %v", err)
	}
	return raw
}

type voipCaptureSender struct {
	mu   sync.Mutex
	sent []push.Notification
}

func (s *voipCaptureSender) Send(_ context.Context, n push.Notification) (push.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, n)
	return push.Result{StatusCode: 200}, nil
}

// 接线：被叫登记了 VoIP token、App 在后台（连接还在、已报 offline），主叫 phone.requestCall，
// 假发送器收到一条 VoIP 推送，解密后的 updates 按被叫授权的 layer 还原出来电与主叫。
func TestRequestCallSendsVoIPPushToBackgroundCallee(t *testing.T) {
	ctx := context.Background()
	calleeKey := [8]byte{0x99, 0x01}
	authKeys := memory.NewAuthKeyStore()
	authorizations := memory.NewAuthorizationStore()
	authorizations.LinkAuthKeyAuthority(authKeys)
	devices := memory.NewPushDeviceStore(authorizations)
	userStore := memory.NewUserStore()
	caller, err := userStore.Create(ctx, domain.User{AccessHash: 9901, Phone: "15550009901", FirstName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	callee, err := userStore.Create(ctx, domain.User{AccessHash: 9902, Phone: "15550009902", FirstName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	if err := authKeys.Save(ctx, store.AuthKeyData{ID: calleeKey}); err != nil {
		t.Fatal(err)
	}
	if err := authorizations.Bind(ctx, domain.Authorization{AuthKeyID: calleeKey, UserID: callee.ID, Hash: 9901}); err != nil {
		t.Fatal(err)
	}
	auth := appauth.NewService(userStore, authorizations, nil, authKeys, nil, "12345")
	// 被叫授权协商过的 layer（auth_keys.layer），VoIP updates 按它序列化。
	if err := auth.UpdateAuthKeyClientInfo(ctx, calleeKey, domain.AuthKeyClientInfo{Layer: 227}); err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{
		Auth:        auth,
		Users:       appusers.NewService(userStore),
		Privacy:     stubPrivacy{},
		Phone:       appphone.NewService(appphone.Config{}),
		Sessions:    &phoneCaptureSessions{},
		PushDevices: devices,
	}, zaptest.NewLogger(t), clock.System)
	dispatch := func(req bin.Encoder) {
		t.Helper()
		var buf bin.Buffer
		if err := req.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Dispatch(ctx, calleeKey, 1, &buf); err != nil {
			t.Fatalf("dispatch %T: %v", req, err)
		}
	}
	const voipToken = "9a9b9c9d9e9f9a9b9c9d9e9f9a9b9c9d9e9f9a9b9c9d9e9f9a9b9c9d9e9f9a9b"
	secret := bytes.Repeat([]byte{9}, push.SecretSize)
	dispatch(&tg.AccountRegisterDeviceRequest{TokenType: domain.PushTokenVoIP, Token: voipToken, Secret: secret})
	// 普通登记不收来电推送。
	dispatch(&tg.AccountRegisterDeviceRequest{TokenType: domain.PushTokenAPNs, Token: "aaaa", Secret: secret})
	dispatch(&tg.AccountUpdateStatusRequest{Offline: true})

	sender := &voipCaptureSender{}
	svc := push.NewService(push.Config{}, push.Deps{Devices: devices, Presence: r, Sender: sender}, zaptest.NewLogger(t))
	r.SetCallPushNotifier(svc)
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(runCtx) }()

	_, gaHash, _ := phoneTestKeys()
	callerCtx := WithSessionID(WithRawAuthKeyID(WithUserID(ctx, caller.ID), [8]byte{0x99, 0x02}), 9902)
	res, err := r.onPhoneRequestCall(callerCtx, &tg.PhoneRequestCallRequest{
		UserID:   &tg.InputUser{UserID: callee.ID, AccessHash: callee.AccessHash},
		RandomID: 1,
		GAHash:   gaHash,
		Protocol: phoneTestProtocol(),
	})
	if err != nil {
		t.Fatalf("requestCall: %v", err)
	}
	stop()
	<-done

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d pushes, want 1 voip push", len(sender.sent))
	}
	n := sender.sent[0]
	if n.Token != voipToken || n.Type != push.PushTypeVoIP || n.Priority != push.PriorityHigh || !n.ExpireImmediately {
		t.Fatalf("push = %+v, want a voip push to the voip token with apns-expiration 0", n)
	}
	requested, updates := decodeVoIPCallUpdates(t, tlprofile.Profile227, voipDecrypt(t, secret, n.Payload))
	if requested.ID != res.PhoneCall.GetID() || requested.AdminID != caller.ID || requested.ParticipantID != callee.ID {
		t.Fatalf("phoneCallRequested = %+v", requested)
	}
	if user, ok := voipUpdatesUser(updates, caller.ID); !ok || user.FirstName != "Alice" {
		t.Fatalf("updates users = %+v, want caller Alice", updates.Users)
	}
}
