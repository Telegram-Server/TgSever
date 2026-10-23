package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

// pushedToBot 返回推给 bot 的全部 update。
func pushedToBot(sessions *phoneCaptureSessions, botUserID int64) []tg.UpdateClass {
	var out []tg.UpdateClass
	for _, rec := range sessions.records() {
		if rec.userID != botUserID {
			continue
		}
		if updates, ok := rec.msg.(*tg.Updates); ok {
			out = append(out, updates.Updates...)
		}
	}
	return out
}

// 超级群里有人改反应（#150）：提交后把带 qts 的 updateBotMessageReaction 推给管理员 bot，
// 离线后从 getDifference 按 qts 补回（带 chats）。
func TestBotMessageReactionPushAndDifference(t *testing.T) {
	f := newBotQtsFixture(t)
	if _, err := f.router.onChannelsInviteToChannel(f.ownerCtx(), &tg.ChannelsInviteToChannelRequest{
		Channel: f.inputChannel(),
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}},
	}); err != nil {
		t.Fatalf("invite bot: %v", err)
	}
	if _, err := f.router.onChannelsEditAdmin(f.ownerCtx(), &tg.ChannelsEditAdminRequest{
		Channel: f.inputChannel(), UserID: &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}, AdminRights: tg.ChatAdminRights{DeleteMessages: true},
	}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}
	sent, err := f.router.deps.Channels.SendMessage(f.ctx, f.owner.ID, domain.SendChannelMessageRequest{ChannelID: f.channel.ID, RandomID: 1, Message: "vote", Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	req := &tg.MessagesSendReactionRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		MsgID: sent.Message.ID,
	}
	req.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "👍"}})
	if _, err := f.router.onMessagesSendReaction(f.ownerCtx(), req); err != nil {
		t.Fatalf("send reaction: %v", err)
	}

	var pushed []*tg.UpdateBotMessageReaction
	for _, update := range pushedToBot(f.sessions, f.bot.ID) {
		if r, ok := update.(*tg.UpdateBotMessageReaction); ok {
			pushed = append(pushed, r)
		}
	}
	if len(pushed) != 1 {
		t.Fatalf("bot received %d updateBotMessageReaction, want 1", len(pushed))
	}
	got := pushed[0]
	if peer, ok := got.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != f.channel.ID || got.MsgID != sent.Message.ID || got.Qts != 4 {
		t.Fatalf("pushed = %+v, want channel %d msg %d qts 4 (after join and promotion)", got, f.channel.ID, sent.Message.ID)
	}
	if actor, ok := got.Actor.(*tg.PeerUser); !ok || actor.UserID != f.owner.ID || len(got.OldReactions) != 0 || len(got.NewReactions) != 1 {
		t.Fatalf("pushed actor/reactions = %+v", got)
	}

	diff, err := f.router.onUpdatesGetDifference(f.botCtx(1), &tg.UpdatesGetDifferenceRequest{Qts: 3, Date: 1})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok || full.State.Qts != 4 || len(full.Chats) == 0 {
		t.Fatalf("difference = %#v, want state qts 4 with chats", diff)
	}
	var replayed []int
	for _, update := range full.OtherUpdates {
		if r, ok := update.(*tg.UpdateBotMessageReaction); ok {
			replayed = append(replayed, r.Qts)
		}
	}
	if len(replayed) != 1 || replayed[0] != 4 {
		t.Fatalf("difference reaction qts = %v, want [4]", replayed)
	}
}

type botReactionMessages struct {
	captureMessages
	botUpdates []domain.BotUpdate
}

func (m *botReactionMessages) SetMessageReactions(ctx context.Context, userID int64, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	res, err := m.captureMessages.SetMessageReactions(ctx, userID, req)
	res.BotUpdates = m.botUpdates
	return res, err
}

// 私聊反应（#150）：store 返回的 bot qts 记录在提交后推给 bot，会话是对方用户。
func TestBotPrivateMessageReactionPush(t *testing.T) {
	const human, bot = int64(1000000101), int64(1000000102)
	messages := &botReactionMessages{botUpdates: []domain.BotUpdate{{
		BotUserID: bot, Qts: 7, Date: 1700000300,
		MessageReaction: &domain.BotMessageReactionUpdate{
			Peer: domain.Peer{Type: domain.PeerTypeUser, ID: human}, MessageID: 42,
			Actor: domain.Peer{Type: domain.PeerTypeUser, ID: human},
			Old:   []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "👎"}},
			New:   []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "👍"}},
		},
	}}}
	sessions := &phoneCaptureSessions{}
	r := New(Config{}, Deps{Messages: messages, Sessions: sessions}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1700000300, 0)})
	req := &tg.MessagesSendReactionRequest{Peer: &tg.InputPeerUser{UserID: bot, AccessHash: 22}, MsgID: 9}
	req.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "👍"}})
	if _, err := r.onMessagesSendReaction(WithUserID(context.Background(), human), req); err != nil {
		t.Fatalf("send reaction: %v", err)
	}
	var pushed []*tg.UpdateBotMessageReaction
	for _, update := range pushedToBot(sessions, bot) {
		if u, ok := update.(*tg.UpdateBotMessageReaction); ok {
			pushed = append(pushed, u)
		}
	}
	if len(pushed) != 1 {
		t.Fatalf("bot received %d updateBotMessageReaction, want 1", len(pushed))
	}
	got := pushed[0]
	peer, _ := got.Peer.(*tg.PeerUser)
	if peer == nil || peer.UserID != human || got.MsgID != 42 || got.Qts != 7 || got.Date != 1700000300 || len(got.OldReactions) != 1 || len(got.NewReactions) != 1 {
		t.Fatalf("pushed = %+v", got)
	}
}
