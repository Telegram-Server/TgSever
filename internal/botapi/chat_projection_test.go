package botapi

import (
	"testing"

	"telesrv/internal/domain"
)

func TestAPIMessageChatDescribesBroadcastChannels(t *testing.T) {
	channel := domain.Channel{ID: 77, Title: "News", Username: "newsroom", Broadcast: true}
	msg := domain.Message{ID: 3, Date: 1, Body: "post", Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 77}}

	chat, _ := apiMessage(msg, nil, []domain.Channel{channel})["chat"].(map[string]any)
	if chat["type"] != "channel" || chat["title"] != "News" || chat["username"] != "newsroom" || chat["id"] != int64(-1000000000077) {
		t.Fatalf("chat = %#v, want a titled broadcast channel", chat)
	}
}

func TestAPIMessageChatKeepsSupergroupsAndUnknownChannels(t *testing.T) {
	group := domain.Channel{ID: 78, Title: "Talk", Megagroup: true}
	msg := domain.Message{ID: 3, Date: 1, Body: "hi", Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 78}}

	chat, _ := apiMessage(msg, nil, []domain.Channel{group})["chat"].(map[string]any)
	if chat["type"] != "supergroup" || chat["title"] != "Talk" {
		t.Fatalf("chat = %#v, want a titled supergroup", chat)
	}
	bare, _ := apiMessage(msg, nil)["chat"].(map[string]any)
	if bare["type"] != "supergroup" {
		t.Fatalf("chat without channel info = %#v, want the supergroup default", bare)
	}
}

func TestLegacyQueuedChannelPostsKeepTheirUpdateKind(t *testing.T) {
	for _, edited := range []bool{false, true} {
		for _, broadcast := range []bool{false, true} {
			event := domain.UpdateEvent{BotAPIUpdateID: 91, Type: domain.UpdateEventNewMessage, Message: domain.Message{ID: 3, Date: 1, Body: "post", Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 77}}, Channels: []domain.Channel{{ID: 77, Broadcast: broadcast, Megagroup: !broadcast}}}
			if edited {
				event.Type = domain.UpdateEventEditMessage
			}
			update, kind, ok := apiUpdate(event)
			want := "message"
			if broadcast {
				want = "channel_post"
			}
			if edited {
				want = "edited_" + want
			}
			if !ok || kind != want || update[want] == nil || len(update) != 2 {
				t.Fatalf("broadcast=%v edited=%v update=%+v kind=%s", broadcast, edited, update, kind)
			}
		}
	}
}
