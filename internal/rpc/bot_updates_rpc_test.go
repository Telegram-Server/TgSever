package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	channelsapp "telesrv/internal/app/channels"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type botQtsFixture struct {
	ctx      context.Context
	router   *Router
	sessions *phoneCaptureSessions
	updates  *memory.BotUpdateStore
	owner    domain.User
	bot      domain.User
	channel  domain.Channel
}

func newBotQtsFixture(t *testing.T) *botQtsFixture {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	botUpdates := memory.NewBotUpdateStore(users)
	channelStore := memory.NewChannelStore()
	channelStore.AttachBotUpdateStore(botUpdates)
	channels := channelsapp.NewService(channelStore)
	sessions := &phoneCaptureSessions{}
	router := New(Config{}, Deps{
		Users:      appusers.NewService(users),
		Updates:    appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Channels:   channels,
		BotUpdates: botUpdates,
		Files:      &fakeFiles{},
		Sessions:   sessions,
	}, zaptest.NewLogger(t), clock.System)
	owner, err := users.Create(ctx, domain.User{AccessHash: 8101, Phone: "13800008101", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := users.Create(ctx, domain.User{AccessHash: 8102, FirstName: "QtsBot", Bot: true})
	if err != nil {
		t.Fatal(err)
	}
	created, err := channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "qts group", Megagroup: true, Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	return &botQtsFixture{ctx: ctx, router: router, sessions: sessions, updates: botUpdates, owner: owner, bot: bot, channel: created.Channel}
}

func (f *botQtsFixture) ownerCtx() context.Context {
	return WithAuthKeyID(WithSessionID(WithUserID(f.ctx, f.owner.ID), 81010), [8]byte{8, 1, 0, 1})
}

// botCtx 是 bot 的第 n 个会话（各自独立的 auth key）。
func (f *botQtsFixture) botCtx(n byte) context.Context {
	return WithAuthKeyID(WithSessionID(WithUserID(f.ctx, f.bot.ID), 81020+int64(n)), [8]byte{8, 1, 0, 2, n})
}

func (f *botQtsFixture) inputChannel() tg.InputChannelClass {
	return &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
}

// pushedBotParticipants 返回推给 bot 的全部 updateChannelParticipant。
func (f *botQtsFixture) pushedBotParticipants(t *testing.T) []*tg.UpdateChannelParticipant {
	t.Helper()
	var out []*tg.UpdateChannelParticipant
	for _, rec := range f.sessions.records() {
		if rec.userID != f.bot.ID {
			continue
		}
		updates, ok := rec.msg.(*tg.Updates)
		if !ok {
			continue
		}
		for _, update := range updates.Updates {
			if p, ok := update.(*tg.UpdateChannelParticipant); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// bot 在线时，被拉进群、被设为管理员各收到一条带 qts 的 updateChannelParticipant（含变化
// 前后的参与者），且不再收到同一变化的无 qts 版本；同一个 bot 的每个会话看到同一条 qts
// 序列，getDifference 按 qts 顺序补回。
func TestBotQtsChannelParticipantPushAndDifference(t *testing.T) {
	f := newBotQtsFixture(t)
	if _, err := f.router.onChannelsInviteToChannel(f.ownerCtx(), &tg.ChannelsInviteToChannelRequest{
		Channel: f.inputChannel(),
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}},
	}); err != nil {
		t.Fatalf("invite bot: %v", err)
	}
	if _, err := f.router.onChannelsEditAdmin(f.ownerCtx(), &tg.ChannelsEditAdminRequest{
		Channel:     f.inputChannel(),
		UserID:      &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		AdminRights: tg.ChatAdminRights{DeleteMessages: true},
	}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}

	pushed := f.pushedBotParticipants(t)
	if len(pushed) != 2 {
		t.Fatalf("bot received %d updateChannelParticipant, want 2 (qts only): %+v", len(pushed), pushed)
	}
	if pushed[0].Qts != 1 || pushed[1].Qts != 2 {
		t.Fatalf("pushed qts = %d,%d, want 1,2", pushed[0].Qts, pushed[1].Qts)
	}
	if _, hasPrev := pushed[0].GetPrevParticipant(); hasPrev {
		t.Fatalf("join update has a previous participant: %+v", pushed[0])
	}
	if np, _ := pushed[0].GetNewParticipant(); np == nil {
		t.Fatalf("join update has no new participant: %+v", pushed[0])
	}
	prev, _ := pushed[1].GetPrevParticipant()
	next, _ := pushed[1].GetNewParticipant()
	if _, ok := prev.(*tg.ChannelParticipantSelf); !ok {
		t.Fatalf("promotion previous = %T, want channelParticipantSelf", prev)
	}
	if _, ok := next.(*tg.ChannelParticipantAdmin); !ok {
		t.Fatalf("promotion new = %T, want channelParticipantAdmin", next)
	}

	for _, session := range []byte{1, 2} {
		state, err := f.router.onUpdatesGetState(f.botCtx(session))
		if err != nil {
			t.Fatalf("session %d getState: %v", session, err)
		}
		if state.Qts != 2 {
			t.Fatalf("session %d getState qts = %d, want 2", session, state.Qts)
		}
	}
	diff, err := f.router.onUpdatesGetDifference(f.botCtx(3), &tg.UpdatesGetDifferenceRequest{Qts: 0, Date: 1})
	if err != nil {
		t.Fatalf("bot getDifference: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("bot getDifference = %T, want updates.difference", diff)
	}
	var qts []int
	for _, update := range full.OtherUpdates {
		if p, ok := update.(*tg.UpdateChannelParticipant); ok {
			qts = append(qts, p.Qts)
		}
	}
	if len(qts) != 2 || qts[0] != 1 || qts[1] != 2 || full.State.Qts != 2 {
		t.Fatalf("difference qts = %v state qts %d, want [1 2] and 2", qts, full.State.Qts)
	}
	if len(full.Chats) == 0 {
		t.Fatal("difference carries no chats for the participant updates")
	}
	again, err := f.router.onUpdatesGetDifference(f.botCtx(3), &tg.UpdatesGetDifferenceRequest{Qts: 2, Date: full.State.Date, Pts: full.State.Pts})
	if err != nil {
		t.Fatalf("bot getDifference from qts 2: %v", err)
	}
	if next, ok := again.(*tg.UpdatesDifference); ok {
		for _, update := range next.OtherUpdates {
			if _, ok := update.(*tg.UpdateChannelParticipant); ok {
				t.Fatalf("difference from the current qts replays %+v", update)
			}
		}
		if next.State.Qts != 2 {
			t.Fatalf("difference from qts 2 moved qts to %d", next.State.Qts)
		}
	}
}

// 请求的 qts 早于最旧的保留记录：返回 differenceTooLong，而不是缺一段的差分。
func TestBotQtsDifferenceTooLongPastRetention(t *testing.T) {
	f := newBotQtsFixture(t)
	old := int(time.Now().Add(-48 * time.Hour).Unix())
	if _, err := f.updates.AppendBotUpdates(f.ctx, []domain.BotUpdate{
		{BotUserID: f.bot.ID, Date: old, ChannelParticipant: &domain.BotChannelParticipantUpdate{Channel: f.channel, Current: domain.ChannelMember{ChannelID: f.channel.ID, UserID: f.bot.ID, Status: domain.ChannelMemberActive, Role: domain.ChannelRoleMember}}},
		{BotUserID: f.bot.ID, Date: int(time.Now().Unix()), ChannelParticipant: &domain.BotChannelParticipantUpdate{Channel: f.channel, Current: domain.ChannelMember{ChannelID: f.channel.ID, UserID: f.bot.ID, Status: domain.ChannelMemberLeft, Role: domain.ChannelRoleMember}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.updates.DeleteExpiredBotUpdates(f.ctx, time.Now().Add(-24*time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	diff, err := f.router.onUpdatesGetDifference(f.botCtx(1), &tg.UpdatesGetDifferenceRequest{Qts: 0, Date: 1})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	if _, ok := diff.(*tg.UpdatesDifferenceTooLong); !ok {
		t.Fatalf("getDifference past retention = %T, want differenceTooLong", diff)
	}
	diff, err = f.router.onUpdatesGetDifference(f.botCtx(1), &tg.UpdatesGetDifferenceRequest{Qts: 1, Date: 1})
	if err != nil {
		t.Fatalf("getDifference from the retained floor: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok || full.State.Qts != 2 || len(full.OtherUpdates) != 1 {
		t.Fatalf("getDifference from qts 1 = %#v, want the retained record at qts 2", diff)
	}
}

// 普通用户的 qts 仍是按设备计数的密聊 qts：接入 bot qts 序列不改变它。
func TestNonBotQtsStaysDeviceSecretChatQts(t *testing.T) {
	f := newEncryptedFixture(t)
	f.router.deps.BotUpdates = memory.NewBotUpdateStore(nil)
	deviceKey := businessAuthKeyInt64(encPartAuthKey)
	for i := 1; i <= 2; i++ {
		if _, _, err := f.queue.AppendEncryptedMessage(f.ctx, domain.SecretChatMessage{
			ReceiverAuthKeyID: deviceKey, ReceiverUserID: f.participant.ID, ChatID: 702,
			RandomID: int64(71000 + i), Date: 1700000000 + i, Bytes: []byte{byte(i)},
		}); err != nil {
			t.Fatalf("append encrypted message %d: %v", i, err)
		}
	}
	state, err := f.router.onUpdatesGetState(f.participantCtx())
	if err != nil || state.Qts != 2 {
		t.Fatalf("participant getState qts = %v, %v; want device qts 2", state, err)
	}
	diff, err := f.router.onUpdatesGetDifference(f.participantCtx(), &tg.UpdatesGetDifferenceRequest{Qts: 1})
	if err != nil {
		t.Fatalf("participant getDifference: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok || len(full.NewEncryptedMessages) != 1 || full.State.Qts != 2 {
		t.Fatalf("participant difference = %#v, want one encrypted message at qts 2", diff)
	}
}
