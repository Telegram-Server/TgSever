package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	appbots "telesrv/internal/app/bots"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestGuestMentionReachesNonmemberBotFromRealGroupSendOnce(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	queries := memory.NewBotGuestQueryStore()
	f.router.deps.BotGuestQueries = queries
	appbots.WithGuestQueries(queries)(f.bots)
	if err := f.bots.SetGuestChat(f.ctx, f.owner.ID, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	created, err := f.channels.CreateMegagroupFromCreateChat(f.ctx, f.owner.ID, domain.CreateChannelRequest{Title: "Guest group", MemberUserIDs: []int64{f.member.ID}, Date: int(f.router.clock.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	peer := &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash}
	request := &tg.MessagesSendMessageRequest{Peer: peer, Message: "@" + f.bot.Username + " help", RandomID: 9201}
	ownerCtx := WithUserID(f.ctx, f.owner.ID)
	if _, err := f.router.onMessagesSendMessage(ownerCtx, request); err != nil {
		t.Fatal(err)
	}
	call := inlineHTTP(t, f)
	updates := call("getUpdates", nil, true).([]any)
	if len(updates) != 1 {
		t.Fatalf("guest updates=%v", updates)
	}
	update := updates[0].(map[string]any)
	guest, ok := update["guest_message"].(map[string]any)
	if !ok {
		t.Fatalf("missing group guest message: %v", update)
	}
	chat := guest["chat"].(map[string]any)
	if chat["type"] != "supergroup" || chat["id"] != float64(-botAPIChannelChatIDBase-created.Channel.ID) {
		t.Fatalf("guest chat=%v", chat)
	}
	call("getUpdates", map[string]any{"offset": int64(update["update_id"].(float64)) + 1}, true)
	if _, err := f.router.onMessagesSendMessage(ownerCtx, request); err != nil {
		t.Fatal(err)
	}
	if got := call("getUpdates", nil, true).([]any); len(got) != 0 {
		t.Fatalf("duplicate send created new guest query: %v", got)
	}
	call("answerGuestQuery", map[string]any{"guest_query_id": guest["guest_query_id"], "result": map[string]any{"type": "article", "id": "answer", "title": "Answer", "input_message_content": map[string]any{"message_text": "Nonmember guest answer"}}}, true)
	history, err := f.channels.GetHistory(f.ctx, f.member.ID, domain.ChannelHistoryFilter{ChannelID: created.Channel.ID, Limit: 1})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("guest history=%+v err=%v", history, err)
	}
	reply := history.Messages[0]
	if reply.Body != "Nonmember guest answer" || reply.From != (domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}) || reply.GuestChatViaFrom != (domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}) {
		t.Fatalf("guest reply=%+v", reply)
	}
	if _, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.bot.ID), &tg.MessagesSendMessageRequest{Peer: peer, Message: "no persistent membership", RandomID: 9202}); err == nil {
		t.Fatal("guest reply granted permanent bot membership")
	}
}
