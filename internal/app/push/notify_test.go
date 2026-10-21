package push

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func intPtr(v int) *int    { return &v }
func boolPtr(v bool) *bool { return &v }

// foreverMute 是客户端“永久静音”时发送的 mute_until（int32 最大值）。
const foreverMute = 2147483647

func TestResolveNotifyMuteMatrix(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	nowUnix := int(now.Unix())
	mutes := []struct {
		name  string
		until int
		muted bool
	}{
		{"forever", foreverMute, true},
		{"timed-pending", nowUnix + 3600, true},
		{"timed-expired", nowUnix - 1, false},
		{"expires-now", nowUnix, false},
	}
	sources := []string{"peer", "default", "none"}
	kinds := []ChatKind{ChatPrivate, ChatGroup, ChatChannel}

	for _, kind := range kinds {
		for _, source := range sources {
			for _, m := range mutes {
				t.Run(fmt.Sprintf("kind%d/%s/%s", kind, source, m.name), func(t *testing.T) {
					var peer, def domain.PeerNotifySettings
					want := m.muted
					switch source {
					case "peer":
						peer.MuteUntil = intPtr(m.until)
					case "default":
						def.MuteUntil = intPtr(m.until)
					case "none":
						want = false
					}
					got := ResolveNotify(peer, def, now)
					if got.Mute != want {
						t.Fatalf("Mute = %v, want %v", got.Mute, want)
					}
				})
			}
		}
	}
}

func TestResolveNotifyPeerSettingOverridesDefaultScope(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	// 会话显式取消静音（mute_until=0）时不再看默认范围的静音。
	got := ResolveNotify(domain.PeerNotifySettings{MuteUntil: intPtr(0)}, domain.PeerNotifySettings{MuteUntil: intPtr(foreverMute)}, now)
	if got.Mute {
		t.Fatal("peer mute_until=0 must override a muted default scope")
	}
	// 会话显式静音时，默认范围未静音也静音。
	got = ResolveNotify(domain.PeerNotifySettings{MuteUntil: intPtr(foreverMute)}, domain.PeerNotifySettings{MuteUntil: intPtr(0)}, now)
	if !got.Mute {
		t.Fatal("peer mute must win over an unmuted default scope")
	}
}

func TestResolveNotifyPreviewAndSilentInheritPerField(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name        string
		peer, def   domain.PeerNotifySettings
		hidePreview bool
		silent      bool
	}{
		{name: "nothing set: previews on, sound on"},
		{name: "default hides preview", def: domain.PeerNotifySettings{ShowPreviews: boolPtr(false)}, hidePreview: true},
		{name: "peer shows preview over default", peer: domain.PeerNotifySettings{ShowPreviews: boolPtr(true)}, def: domain.PeerNotifySettings{ShowPreviews: boolPtr(false)}},
		{name: "peer hides preview", peer: domain.PeerNotifySettings{ShowPreviews: boolPtr(false)}, hidePreview: true},
		{name: "default silent", def: domain.PeerNotifySettings{Silent: boolPtr(true)}, silent: true},
		{name: "peer not silent over default", peer: domain.PeerNotifySettings{Silent: boolPtr(false)}, def: domain.PeerNotifySettings{Silent: boolPtr(true)}},
		// 会话只设了静音时间，预览与静默仍继承默认范围。
		{name: "peer mute only, rest inherits", peer: domain.PeerNotifySettings{MuteUntil: intPtr(0)}, def: domain.PeerNotifySettings{ShowPreviews: boolPtr(false), Silent: boolPtr(true)}, hidePreview: true, silent: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveNotify(tc.peer, tc.def, now)
			if got.HidePreview != tc.hidePreview || got.Silent != tc.silent {
				t.Fatalf("got %+v, want HidePreview=%v Silent=%v", got, tc.hidePreview, tc.silent)
			}
		})
	}
}

func TestHiddenPreviewPayloadCarriesNoMessageContent(t *testing.T) {
	msg := NewMessage{Chat: Chat{Kind: ChatPrivate, ID: 1000042}, SenderID: 1000042, MessageID: 77, Text: "top secret words"}
	key, args := messageAlert(msg, "Alice", false)
	if key != KeyMessageNoText {
		t.Fatalf("key = %s, want %s when previews are hidden", key, KeyMessageNoText)
	}
	alert := NewRenderer(testLangPack()).Render(context.Background(), "en", key, args...)
	secret := testSecret()
	payload, err := buildMessagePayload(MessagePush{Chat: msg.Chat, MessageID: msg.MessageID, Key: key, Alert: alert}, secret, "You have a new message")
	if err != nil {
		t.Fatal(err)
	}
	var outer struct {
		P string `json:"p"`
	}
	if err := json.Unmarshal(payload, &outer); err != nil {
		t.Fatal(err)
	}
	plain, _, err := nseDecrypt(secret, outer.P)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "top secret") || strings.Contains(string(payload), "top secret") {
		t.Fatalf("payload with hidden preview leaks text: %s", plain)
	}
	inner := decodeJSONObject(t, plain)
	got := inner["aps"].(map[string]any)["alert"].(map[string]any)
	if got["title"] != "Alice" || got["body"] != "New message" {
		t.Fatalf("alert = %v, want the rendered MESSAGE_NOTEXT text", got)
	}
}

func TestNotifyScopesForChat(t *testing.T) {
	cases := []struct {
		chat     Chat
		peer     domain.Peer
		defaults domain.NotifyScopeKind
	}{
		{Chat{Kind: ChatPrivate, ID: 42}, domain.Peer{Type: domain.PeerTypeUser, ID: 42}, domain.NotifyScopeUsers},
		{Chat{Kind: ChatGroup, ID: 7}, domain.Peer{Type: domain.PeerTypeChannel, ID: 7}, domain.NotifyScopeChats},
		{Chat{Kind: ChatChannel, ID: 9}, domain.Peer{Type: domain.PeerTypeChannel, ID: 9}, domain.NotifyScopeBroadcasts},
	}
	for _, tc := range cases {
		peer, def := NotifyScopes(tc.chat)
		if peer != (domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: tc.peer}) || def != (domain.NotifyScope{Kind: tc.defaults}) {
			t.Fatalf("NotifyScopes(%+v) = %+v, %+v", tc.chat, peer, def)
		}
	}
}
