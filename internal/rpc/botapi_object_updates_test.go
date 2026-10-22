package rpc

import (
	"context"
	"encoding/json"
	"github.com/iamxvbaba/td/tg"
	"net/http"
	"net/http/httptest"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func botAPIObjectBody(t *testing.T, event domain.UpdateEvent) map[string]any {
	t.Helper()
	if event.BotAPIObject == nil {
		t.Fatalf("missing public snapshot: %+v", event)
	}
	body, err := event.BotAPIObject.Object()
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBotAPIMessageSnapshotSurvivesEditBeforeHTTPPolling(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sent, err := f.messages.SendPrivateText(f.ctx, f.owner.ID, domain.SendPrivateTextRequest{RecipientUserID: f.bot.ID, RandomID: 98101, Message: "original", Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	f.router.enqueueBotAPIPrivateMessageUpdate(f.ctx, sent)
	edited, err := f.messages.EditMessage(f.ctx, f.owner.ID, domain.EditMessageRequest{OwnerUserID: f.owner.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, ID: sent.SenderMessage.ID, Message: "edited", EditDate: 1700000001})
	if err != nil {
		t.Fatal(err)
	}
	f.router.enqueueBotAPIPrivateEditUpdates(f.ctx, edited)
	updates := joinHTTPUpdates(t, f)
	if len(updates) != 2 {
		t.Fatalf("updates=%+v", updates)
	}
	first, ok := updates[0]["message"].(map[string]any)
	if !ok || first["text"] != "original" {
		t.Fatalf("original was rewritten: %+v", updates[0])
	}
	second, ok := updates[1]["edited_message"].(map[string]any)
	if !ok || second["text"] != "edited" || first["message_id"] != second["message_id"] || updates[1]["update_id"].(float64) <= updates[0]["update_id"].(float64) {
		t.Fatalf("edit transition=%+v", updates)
	}
	if _, err := f.router.BotAPIUpdates(f.ctx, f.bot.ID, int64(updates[1]["update_id"].(float64))+1); err != nil {
		t.Fatal(err)
	}
	if got := joinHTTPUpdates(t, f); len(got) != 0 {
		t.Fatalf("ack replay=%+v", got)
	}
}

func TestBotAPIChannelPostIsNotAnOrdinaryMessage(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	created, err := f.channels.CreateChannel(f.ctx, f.owner.ID, domain.CreateChannelRequest{Title: "News", Broadcast: true, MemberUserIDs: []int64{f.bot.ID}, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	sent, err := f.channels.SendMessage(f.ctx, f.owner.ID, domain.SendChannelMessageRequest{UserID: f.owner.ID, ChannelID: created.Channel.ID, RandomID: 98102, Message: "broadcast", Date: 1700000001, SkipRecipientLookup: true})
	if err != nil {
		t.Fatal(err)
	}
	f.router.enqueueBotAPIChannelMessageUpdateForBots(f.ctx, sent, []int64{f.bot.ID})
	edited, err := f.channels.EditMessage(f.ctx, f.owner.ID, domain.EditChannelMessageRequest{UserID: f.owner.ID, ChannelID: created.Channel.ID, ID: sent.Message.ID, Message: "broadcast edit", EditDate: 1700000002})
	if err != nil {
		t.Fatal(err)
	}
	f.router.enqueueBotAPIChannelEditMessageUpdate(f.ctx, f.owner.ID, edited)
	updates := joinHTTPUpdates(t, f)
	if len(updates) != 2 {
		t.Fatalf("channel updates=%+v", updates)
	}
	first, ok := updates[0]["channel_post"].(map[string]any)
	if !ok || first["text"] != "broadcast" || first["chat"].(map[string]any)["type"] != "channel" {
		t.Fatalf("channel post=%+v", updates[0])
	}
	if _, wrong := updates[0]["message"]; wrong {
		t.Fatalf("broadcast leaked as message=%+v", updates[0])
	}
	second, ok := updates[1]["edited_channel_post"].(map[string]any)
	if !ok || second["text"] != "broadcast edit" {
		t.Fatalf("channel edit=%+v", updates[1])
	}
}

func TestBotAPIQueuedMessageIsRevokedAfterLeavingChannel(t *testing.T) {
	f := newBotAPIReceiveFixture(t, false)
	sent, err := f.channels.SendMessage(f.ctx, f.owner.ID, domain.SendChannelMessageRequest{UserID: f.owner.ID, ChannelID: f.channel.ID, RandomID: 98103, Message: "/private command", Date: 1700000000, SkipRecipientLookup: true})
	if err != nil {
		t.Fatal(err)
	}
	f.router.enqueueBotAPIChannelMessageUpdateForBots(f.ctx, sent, []int64{f.bot.ID})
	rows, err := f.router.deps.BotAPIUpdates.ListBotAPIUpdates(f.ctx, f.bot.ID, 1, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("queued=%+v err=%v", rows, err)
	}
	if _, err := f.channels.LeaveChannel(f.ctx, f.bot.ID, f.channel.ID, int(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	events, err := f.router.BotAPIUpdates(f.ctx, f.bot.ID, 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("revoked events=%+v err=%v", events, err)
	}
	confirmed, found, err := f.router.deps.BotAPIUpdates.ConfirmedBotAPIUpdateID(f.ctx, f.bot.ID)
	if err != nil || !found || confirmed != rows[0].ID {
		t.Fatalf("revocation ack=%d found=%v err=%v", confirmed, found, err)
	}
}

func TestBotAPIQueuedReactionIsRevokedAfterDemotion(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	subscribeReactionBot(t, f)
	f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
	id := f.sendAs(t, f.owner.ID, "react", 98104)
	sendBridgeReaction(t, f, f.member.ID, &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}, id, &tg.ReactionEmoji{Emoticon: "👍"})
	queued, err := f.router.deps.BotAPIUpdates.ListBotAPIUpdates(f.ctx, f.bot.ID, 1, 100)
	if err != nil || len(queued) != 1 || queued[0].Reaction == nil {
		t.Fatalf("queued=%+v err=%v", queued, err)
	}
	if _, err := f.router.onChannelsEditAdmin(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsEditAdminRequest{
		Channel:     f.inputChannel(),
		UserID:      &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		AdminRights: tg.ChatAdminRights{},
	}); err != nil {
		t.Fatalf("demote bot: %v", err)
	}
	view, err := f.channels.GetChannel(f.ctx, f.bot.ID, f.channel.ID)
	if err != nil || view.Self.Role != domain.ChannelRoleMember {
		t.Fatalf("demotion did not remove admin role: %+v err=%v", view.Self, err)
	}
	if events := reactionBotEvents(t, f); len(events) != 0 {
		t.Fatalf("demoted bot received=%+v", events)
	}
}

func TestBotAPIObjectExpirationDoesNotAdvanceAccountPTS(t *testing.T) {
	object, err := domain.NewBotAPIObject("inline_query", map[string]any{"id": "q", "from": map[string]any{"id": 2, "is_bot": false, "first_name": "Caller"}, "query": "", "offset": ""})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	item := domain.BotAPIUpdate{ID: 1, BotUserID: 3, Kind: "inline_query", Date: int(now.Unix()), Object: object}
	event, ok := botAPIQueuedUpdateEventFromMessages(3, item, nil, nil, now)
	if !ok || event.Pts != 0 || event.BotAPIUpdateID != 1 || botAPIObjectBody(t, event)["id"] != "q" {
		t.Fatalf("object event=%+v ok=%v", event, ok)
	}
	if _, ok := botAPIQueuedUpdateEventFromMessages(3, item, nil, nil, now.Add(24*time.Hour)); ok {
		t.Fatal("expired query delivered")
	}
	item.Object.Payload = json.RawMessage(`{"id":"q","query":"","offset":""}`)
	if _, ok := botAPIQueuedUpdateEventFromMessages(3, item, nil, nil, now); ok {
		t.Fatal("malformed query delivered")
	}
}

func TestBotAPIWebhookDeliversProducerSnapshotAndAcknowledges(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	received := make(chan map[string]any, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var update map[string]any
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case received <- update:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()
	sent, err := f.messages.SendPrivateText(f.ctx, f.owner.ID, domain.SendPrivateTextRequest{RecipientUserID: f.bot.ID, RandomID: 98105, Message: "webhook original", Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	f.router.enqueueBotAPIPrivateMessageUpdate(f.ctx, sent)
	if _, err := f.messages.EditMessage(f.ctx, f.owner.ID, domain.EditMessageRequest{OwnerUserID: f.owner.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, ID: sent.SenderMessage.ID, Message: "edited after queue", EditDate: 1700000001}); err != nil {
		t.Fatal(err)
	}
	if err := f.router.BotAPISetWebhook(f.ctx, domain.BotAPIWebhook{BotUserID: f.bot.ID, URL: endpoint.URL, MaxConnections: 1}, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.bots, f.users, nil, nil, f.router, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var update map[string]any
	select {
	case update = <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("webhook did not receive queued producer event")
	}
	message, ok := update["message"].(map[string]any)
	if !ok || message["text"] != "webhook original" {
		t.Fatalf("webhook snapshot=%+v", update)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		confirmed, found, err := f.router.deps.BotAPIUpdates.ConfirmedBotAPIUpdateID(f.ctx, f.bot.ID)
		if err != nil {
			t.Fatal(err)
		}
		if found && confirmed == int64(update["update_id"].(float64)) {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("successful webhook was not acknowledged")
		}
	}
}

func TestBotAPIPollVoteRetractAndStopProduceSnapshots(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	poll := domain.MessagePoll{Question: "Choose", PublicVoters: true, Answers: []domain.MessagePollAnswer{{Text: "A", Option: []byte{0}}, {Text: "B", Option: []byte{1}}}}
	sent, err := f.router.BotAPISendPoll(f.ctx, f.bot.ID, f.owner.ID, poll, nil, "", nil, nil, false, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	history, err := f.messages.GetHistory(f.ctx, f.owner.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, Limit: 1})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("poll recipient history=%+v err=%v", history, err)
	}
	userMessageID := history.Messages[0].ID
	for _, options := range [][][]byte{{{0}}, {}} {
		if _, err := f.router.onMessagesSendVote(WithUserID(f.ctx, f.owner.ID), &tg.MessagesSendVoteRequest{Peer: &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}, MsgID: userMessageID, Options: options}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.router.BotAPIStopPoll(f.ctx, f.bot.ID, f.owner.ID, sent.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	updates := joinHTTPUpdates(t, f)
	var polls, answers []map[string]any
	for _, update := range updates {
		if body, ok := update["poll"].(map[string]any); ok {
			polls = append(polls, body)
		}
		if body, ok := update["poll_answer"].(map[string]any); ok {
			answers = append(answers, body)
		}
	}
	if len(polls) != 3 || polls[0]["total_voter_count"] != float64(1) || polls[1]["total_voter_count"] != float64(0) || polls[2]["is_closed"] != true {
		t.Fatalf("poll transitions=%+v", polls)
	}
	if len(answers) != 2 {
		t.Fatalf("answers=%+v", answers)
	}
	chosen := answers[0]["option_ids"].([]any)
	if len(chosen) != 1 || chosen[0] != float64(0) || len(answers[1]["option_ids"].([]any)) != 0 || len(answers[1]["option_persistent_ids"].([]any)) != 0 {
		t.Fatalf("vote/retraction=%+v", answers)
	}
}
