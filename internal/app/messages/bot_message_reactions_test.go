package messages

import (
	"context"
	"reflect"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 私聊里用户改反应（#150）：bot 收到带旧反应与新反应的 updateBotMessageReaction，消息 id 是
// bot 自己消息盒里的 id，会话是对方用户；bot 自己设的反应、没有变化的重复设置不产生记录。
func TestPrivateReactionChangesReachBot(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	updates := memory.NewBotUpdateStore(users)
	dialogs := memory.NewDialogStore()
	st := memory.NewMessageStore(dialogs)
	st.AttachBotUpdateStore(updates)
	svc := NewService(st, dialogs)
	newUser := func(bot bool) int64 {
		t.Helper()
		u, err := users.Create(ctx, domain.User{AccessHash: 5, FirstName: "u", Bot: bot})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	human, bot, other := newUser(false), newUser(true), newUser(false)
	const date = 1_700_000_000
	// 先让 bot 发一条无关消息，使双方消息盒的 id 不同。
	if _, err := svc.SendPrivateText(ctx, bot, domain.SendPrivateTextRequest{SenderUserID: bot, RecipientUserID: other, RandomID: 1, Message: "x", Date: date}); err != nil {
		t.Fatal(err)
	}
	sent, err := svc.SendPrivateText(ctx, bot, domain.SendPrivateTextRequest{SenderUserID: bot, RecipientUserID: human, RandomID: 2, Message: "vote", Date: date})
	if err != nil {
		t.Fatal(err)
	}
	botMsgID, humanMsgID := sent.SenderMessage.ID, sent.RecipientMessage.ID
	if humanMsgID == 0 || humanMsgID == botMsgID {
		t.Fatalf("missing recipient message: %+v", sent)
	}
	react := func(userID, peerID int64, msgID int, e ...string) {
		t.Helper()
		if _, err := svc.SetMessageReactions(ctx, userID, domain.SetPrivateMessageReactionsRequest{
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peerID}, MessageID: msgID, Reactions: emoji(e...), ReactionsPerUserMax: 3, Date: date,
		}); err != nil {
			t.Fatalf("react: %v", err)
		}
	}
	react(human, bot, humanMsgID, "👍")
	react(human, bot, humanMsgID, "👍")
	react(human, bot, humanMsgID)
	react(bot, human, botMsgID, "🔥")

	rows, err := updates.ListBotUpdates(ctx, bot, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ old, new []domain.MessageReaction }{{nil, emoji("👍")}, {emoji("👍"), nil}}
	if len(rows) != len(want) {
		t.Fatalf("bot updates = %+v, want %d", rows, len(want))
	}
	for i, w := range want {
		r := rows[i].MessageReaction
		if r == nil {
			t.Fatalf("update %d = %+v", i, rows[i])
		}
		human := domain.Peer{Type: domain.PeerTypeUser, ID: human}
		if r.Peer != human || r.Actor != human || r.MessageID != botMsgID || rows[i].Date != date {
			t.Fatalf("update %d header = %+v date %d, want peer/actor %v msg %d", i, r, rows[i].Date, human, botMsgID)
		}
		if !sameReactions(r.Old, w.old) || !sameReactions(r.New, w.new) {
			t.Fatalf("update %d = %v -> %v, want %v -> %v", i, r.Old, r.New, w.old, w.new)
		}
	}
}

func emoji(e ...string) []domain.MessageReaction {
	out := make([]domain.MessageReaction, 0, len(e))
	for _, v := range e {
		out = append(out, domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: v})
	}
	return out
}

func sameReactions(a, b []domain.MessageReaction) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
