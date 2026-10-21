package push

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// fakeClock 是可拨动的时钟，用来验证定时静音到期后恢复推送。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type notifyFixture struct {
	*serviceFixture
	settings *memory.PasswordStore
	clock    *fakeClock
}

// newNotifyFixture 在基础 fixture 上接入基于内存通知设置存储的 SettingsPolicy。
func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()
	base := newServiceFixture(t, Config{})
	f := &notifyFixture{
		serviceFixture: base,
		settings:       memory.NewPasswordStore(),
		clock:          &fakeClock{now: time.Unix(1_800_000_000, 0)},
	}
	base.svc = NewService(Config{}, Deps{
		Devices:  base.devices,
		Presence: base.presence,
		Users:    fakeUsers{aliceID: {ID: aliceID, FirstName: "Alice", LastName: "A"}, bobID: {ID: bobID, FirstName: "Bob"}},
		Sender:   base.sender,
		Strings:  testLangPack(),
		Policy:   NewSettingsPolicy(f.settings, f.clock.Now),
	}, zaptest.NewLogger(t))
	return f
}

func (f *notifyFixture) save(owner int64, scope domain.NotifyScope, s domain.PeerNotifySettings) {
	f.t.Helper()
	if err := f.settings.SaveNotifySettings(context.Background(), owner, scope, s); err != nil {
		f.t.Fatal(err)
	}
}

// pushOnce 把一条消息交给新启动的 worker，等处理完后返回这期间发出的推送。
func (f *notifyFixture) pushOnce(msg NewMessage) []Notification {
	f.t.Helper()
	f.sender.mu.Lock()
	f.sender.sent = nil
	f.sender.mu.Unlock()
	f.start()
	if !f.svc.NewMessage(msg) {
		f.t.Fatal("NewMessage rejected, want queued")
	}
	f.stop()
	f.sender.mu.Lock()
	defer f.sender.mu.Unlock()
	return append([]Notification(nil), f.sender.sent...)
}

// decryptInner 解出推送 p 字段里的 JSON。
func decryptInner(t *testing.T, secret []byte, n Notification) map[string]any {
	t.Helper()
	var outer struct {
		P string `json:"p"`
	}
	if err := json.Unmarshal(n.Payload, &outer); err != nil || outer.P == "" {
		t.Fatalf("payload %s: want encrypted p (err %v)", n.Payload, err)
	}
	plain, _, err := nseDecrypt(secret, outer.P)
	if err != nil {
		t.Fatal(err)
	}
	return decodeJSONObject(t, plain)
}

var aliceChatScope = domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: aliceID}}

func TestNewMessageSkipsMutedChat(t *testing.T) {
	f := newNotifyFixture(t)
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", testSecret(), presenceNone)
	f.save(bobID, aliceChatScope, domain.PeerNotifySettings{MuteUntil: intPtr(foreverMute)})
	if got := f.pushOnce(textFromAlice("hi")); len(got) != 0 {
		t.Fatalf("pushed %d notifications for a muted chat, want none", len(got))
	}
}

func TestNewMessageSkipsChatMutedByDefaultScope(t *testing.T) {
	f := newNotifyFixture(t)
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", testSecret(), presenceNone)
	f.save(bobID, domain.NotifyScope{Kind: domain.NotifyScopeUsers}, domain.PeerNotifySettings{MuteUntil: intPtr(foreverMute)})
	if got := f.pushOnce(textFromAlice("hi")); len(got) != 0 {
		t.Fatalf("pushed %d notifications with private chats muted by default, want none", len(got))
	}
	// 会话自己取消静音时覆盖默认范围。
	f.save(bobID, aliceChatScope, domain.PeerNotifySettings{MuteUntil: intPtr(0)})
	if got := f.pushOnce(textFromAlice("hi")); len(got) != 1 {
		t.Fatalf("pushed %d notifications for an explicitly unmuted chat, want 1", len(got))
	}
}

func TestNewMessageResumesAfterTimedMuteExpires(t *testing.T) {
	f := newNotifyFixture(t)
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", testSecret(), presenceNone)
	until := f.clock.Now().Add(time.Hour)
	f.save(bobID, aliceChatScope, domain.PeerNotifySettings{MuteUntil: intPtr(int(until.Unix()))})

	if got := f.pushOnce(textFromAlice("during mute")); len(got) != 0 {
		t.Fatalf("pushed %d notifications while the timed mute is pending, want none", len(got))
	}
	f.clock.Set(until.Add(time.Second))
	if got := f.pushOnce(textFromAlice("after mute")); len(got) != 1 {
		t.Fatalf("pushed %d notifications after the timed mute expired, want 1", len(got))
	}
}

func TestNewMessageHiddenPreviewOmitsText(t *testing.T) {
	f := newNotifyFixture(t)
	secret := testSecret()
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", secret, presenceNone)
	f.save(bobID, aliceChatScope, domain.PeerNotifySettings{ShowPreviews: boolPtr(false)})

	got := f.pushOnce(textFromAlice("private words"))
	if len(got) != 1 {
		t.Fatalf("pushed %d notifications, want 1", len(got))
	}
	inner := decryptInner(t, secret, got[0])
	raw, _ := json.Marshal(inner)
	if strings.Contains(string(raw), "private words") || strings.Contains(string(got[0].Payload), "private words") {
		t.Fatalf("payload %s leaks the message text with previews hidden", raw)
	}
	alert := inner["aps"].(map[string]any)["alert"].(map[string]any)
	if alert["title"] != "Alice A" || alert["body"] != "New message" {
		t.Fatalf("alert = %v, want the NOTEXT rendering", alert)
	}
}

func TestNewMessageSilentOmitsSound(t *testing.T) {
	cases := []struct {
		name     string
		silent   bool
		settings *domain.PeerNotifySettings
	}{
		{name: "message sent silently", silent: true},
		{name: "chat set to silent", settings: &domain.PeerNotifySettings{Silent: boolPtr(true)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newNotifyFixture(t)
			secret := testSecret()
			f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", secret, presenceNone)
			if tc.settings != nil {
				f.save(bobID, aliceChatScope, *tc.settings)
			}
			msg := textFromAlice("shh")
			msg.Silent = tc.silent
			got := f.pushOnce(msg)
			if len(got) != 1 {
				t.Fatalf("pushed %d notifications, want 1", len(got))
			}
			inner := decryptInner(t, secret, got[0])
			aps := inner["aps"].(map[string]any)
			if inner["silent"] != "1" {
				t.Fatalf("inner = %v, want silent \"1\"", inner)
			}
			if _, ok := aps["sound"]; ok {
				t.Fatalf("inner aps = %v, want no sound", aps)
			}
		})
	}
}

func TestNewMessageNotSilentByDefaultHasSound(t *testing.T) {
	f := newNotifyFixture(t)
	secret := testSecret()
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", secret, presenceNone)
	got := f.pushOnce(textFromAlice("ring"))
	if len(got) != 1 {
		t.Fatalf("pushed %d notifications, want 1", len(got))
	}
	inner := decryptInner(t, secret, got[0])
	if _, ok := inner["silent"]; ok || inner["aps"].(map[string]any)["sound"] != "default" {
		t.Fatalf("inner = %v, want default sound and no silent flag", inner)
	}
}

func TestNewMessageSkipsDevicesListingSenderInOtherUIDs(t *testing.T) {
	f := newNotifyFixture(t)
	ctx := context.Background()
	// 设备 2：同一台 iPhone 上也登录着 Alice，Alice 发来的消息不推；设备 3：别的手机，照常推。
	for _, d := range []struct {
		key    [8]byte
		token  string
		others []int64
	}{
		{[8]byte{2}, "tok-shared-phone", []int64{aliceID}},
		{[8]byte{3}, "tok-other-phone", []int64{1000099}},
	} {
		if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: d.key, UserID: bobID, Hash: int64(d.key[0])}); err != nil {
			t.Fatal(err)
		}
		if err := f.devices.RegisterPushDevice(ctx, domain.PushDevice{AuthKeyID: d.key, UserID: bobID, TokenType: domain.PushTokenAPNs, Token: d.token, OtherUIDs: d.others}); err != nil {
			t.Fatal(err)
		}
	}
	got := f.pushOnce(textFromAlice("hi"))
	if len(got) != 1 || got[0].Token != "tok-other-phone" {
		tokens := make([]string, 0, len(got))
		for _, n := range got {
			tokens = append(tokens, n.Token)
		}
		t.Fatalf("pushed to %v, want only tok-other-phone", tokens)
	}
}
