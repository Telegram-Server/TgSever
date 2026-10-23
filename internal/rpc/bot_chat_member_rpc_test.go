package rpc

import (
	"context"
	"errors"
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

// 其他成员的变动在提交后推给管理员 bot（#146）：邀请链接入群、入群申请被批准（单个与批量）、
// 封禁各推一条带 qts 的 updateChannelParticipant，user_id 是变动的成员；chat 按 bot 自己的
// 身份投影，封禁的是别人时 bot 仍看到频道本身；封禁不再附带无 qts 的同一变化。不是管理员的
// bot 收不到任何 updateChannelParticipant（反例）。
// adminBotAudienceFixture 是带 bot qts 序列的最小 Router（#146）。
type adminBotAudienceFixture struct {
	router     *Router
	users      *memory.UserStore
	botUpdates *memory.BotUpdateStore
	channels   *channelsapp.Service
	sessions   *phoneCaptureSessions
}

func newAdminBotAudienceFixture(t *testing.T) *adminBotAudienceFixture {
	t.Helper()
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
	return &adminBotAudienceFixture{router: router, users: users, botUpdates: botUpdates, channels: channels, sessions: sessions}
}

func (f *adminBotAudienceFixture) newUser(t *testing.T, hash int64, bot bool) domain.User {
	t.Helper()
	u, err := f.users.Create(context.Background(), domain.User{AccessHash: hash, FirstName: "u", Bot: bot})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (f *adminBotAudienceFixture) userCtx(u domain.User) context.Context {
	return WithAuthKeyID(WithSessionID(WithUserID(context.Background(), u.ID), 82000+u.ID), [8]byte{8, 2, byte(u.ID)})
}

// pushedParticipants 返回推给 userID 的全部 updateChannelParticipant。
func (f *adminBotAudienceFixture) pushedParticipants(userID int64) []*tg.UpdateChannelParticipant {
	var out []*tg.UpdateChannelParticipant
	for _, rec := range f.sessions.records() {
		if rec.userID != userID {
			continue
		}
		if updates, ok := rec.msg.(*tg.Updates); ok {
			for _, update := range updates.Updates {
				if p, ok := update.(*tg.UpdateChannelParticipant); ok {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func TestAdminBotReceivesOtherMemberChangesOnline(t *testing.T) {
	ctx := context.Background()
	fx := newAdminBotAudienceFixture(t)
	router, botUpdates, channels, sessions := fx.router, fx.botUpdates, fx.channels, fx.sessions
	newUser := func(hash int64, bot bool) domain.User { return fx.newUser(t, hash, bot) }
	userCtx := fx.userCtx
	owner, bot, plainBot := newUser(8201, false), newUser(8202, true), newUser(8206, true)
	linkJoiner, requester, batchRequester := newUser(8203, false), newUser(8204, false), newUser(8205, false)
	now := int(time.Now().Unix())
	created, err := channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "audience", Megagroup: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	channel := created.Channel
	if _, err := channels.InviteToChannel(ctx, owner.ID, channel.ID, []int64{plainBot.ID}, now); err != nil {
		t.Fatalf("invite non-admin bot: %v", err)
	}
	if _, err := channels.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: channel.ID, MemberID: bot.ID, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: now}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}
	link, err := channels.ExportInvite(ctx, owner.ID, domain.ExportChannelInviteRequest{UserID: owner.ID, ChannelID: channel.ID, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := channels.ExportInvite(ctx, owner.ID, domain.ExportChannelInviteRequest{UserID: owner.ID, ChannelID: channel.ID, RequestNeeded: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := botUpdates.BotUpdatesState(ctx, bot.ID)
	peer := &tg.InputPeerChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}

	if _, err := router.onMessagesImportChatInvite(userCtx(linkJoiner), link.Invite.Hash); err != nil {
		t.Fatalf("join by link: %v", err)
	}
	for _, u := range []domain.User{requester, batchRequester} {
		if _, err := channels.ImportInvite(ctx, u.ID, domain.ImportChannelInviteRequest{UserID: u.ID, Hash: approval.Invite.Hash, Date: now}); !errors.Is(err, domain.ErrInviteRequestSent) {
			t.Fatalf("request to join: %v", err)
		}
	}
	if _, err := router.onMessagesHideChatJoinRequest(userCtx(owner), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true, Peer: peer, UserID: &tg.InputUser{UserID: requester.ID, AccessHash: requester.AccessHash},
	}); err != nil {
		t.Fatalf("approve request: %v", err)
	}
	if _, err := router.onMessagesHideAllChatJoinRequests(userCtx(owner), &tg.MessagesHideAllChatJoinRequestsRequest{Approved: true, Peer: peer}); err != nil {
		t.Fatalf("approve all requests: %v", err)
	}
	if _, err := router.onChannelsEditBanned(userCtx(owner), &tg.ChannelsEditBannedRequest{
		Channel:      &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash},
		Participant:  &tg.InputPeerUser{UserID: linkJoiner.ID, AccessHash: linkJoiner.AccessHash},
		BannedRights: tg.ChatBannedRights{ViewMessages: true},
	}); err != nil {
		t.Fatalf("ban: %v", err)
	}

	type pushed struct {
		update *tg.UpdateChannelParticipant
		chats  []tg.ChatClass
	}
	var got []pushed
	for _, rec := range sessions.records() {
		if rec.userID == plainBot.ID {
			if updates, ok := rec.msg.(*tg.Updates); ok {
				for _, update := range updates.Updates {
					if p, ok := update.(*tg.UpdateChannelParticipant); ok {
						// 无 qts 的 updateChannelParticipant 会被 TDLib 当成错误 qts 丢弃并触发 getDifference。
						t.Fatalf("non-admin bot received another member's change: %+v", p)
					}
				}
			}
		}
		if rec.userID != bot.ID {
			continue
		}
		updates, ok := rec.msg.(*tg.Updates)
		if !ok {
			continue
		}
		for _, update := range updates.Updates {
			if p, ok := update.(*tg.UpdateChannelParticipant); ok {
				got = append(got, pushed{update: p, chats: updates.Chats})
			}
		}
	}
	wantUsers := []int64{linkJoiner.ID, requester.ID, batchRequester.ID, linkJoiner.ID}
	if len(got) != len(wantUsers) {
		t.Fatalf("admin bot received %d updateChannelParticipant, want %d (qts only): %+v", len(got), len(wantUsers), got)
	}
	for i, p := range got {
		if p.update.UserID != wantUsers[i] || p.update.Qts != before.Qts+i+1 {
			t.Fatalf("push %d: user %d qts %d, want user %d qts %d", i, p.update.UserID, p.update.Qts, wantUsers[i], before.Qts+i+1)
		}
		if len(p.chats) != 1 {
			t.Fatalf("push %d chats = %+v, want the channel", i, p.chats)
		}
		if _, ok := p.chats[0].(*tg.Channel); !ok {
			t.Fatalf("push %d chat = %T, want the channel as the admin bot sees it", i, p.chats[0])
		}
	}
	if next, _ := got[3].update.GetNewParticipant(); next == nil {
		t.Fatal("ban update has no new participant")
	} else if _, ok := next.(*tg.ChannelParticipantBanned); !ok {
		t.Fatalf("ban new participant = %T, want channelParticipantBanned", next)
	}
}

// bot 自己经普通邀请链接入群（#146）：提交后 bot 自己收到一条带 qts 的 updateChannelParticipant
// （my_chat_member），群里的管理员 bot 也收到一条（chat_member），各只推一次。
func TestBotJoiningByLinkPushesEveryQtsRecordOnce(t *testing.T) {
	ctx := context.Background()
	fx := newAdminBotAudienceFixture(t)
	owner, adminBot, joiner := fx.newUser(t, 8401, false), fx.newUser(t, 8402, true), fx.newUser(t, 8403, true)
	now := int(time.Now().Unix())
	created, err := fx.channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "link", Megagroup: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fx.channels.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: created.Channel.ID, MemberID: adminBot.ID, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	link, err := fx.channels.ExportInvite(ctx, owner.ID, domain.ExportChannelInviteRequest{UserID: owner.ID, ChannelID: created.Channel.ID, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	adminBefore := len(fx.pushedParticipants(adminBot.ID))
	if _, err := fx.router.onMessagesImportChatInvite(fx.userCtx(joiner), link.Invite.Hash); err != nil {
		t.Fatalf("bot joins by link: %v", err)
	}
	for _, tc := range []struct {
		name   string
		bot    int64
		pushed []*tg.UpdateChannelParticipant
	}{
		{"joining bot", joiner.ID, fx.pushedParticipants(joiner.ID)},
		{"admin bot", adminBot.ID, fx.pushedParticipants(adminBot.ID)[adminBefore:]},
	} {
		st, _ := fx.botUpdates.BotUpdatesState(ctx, tc.bot)
		if len(tc.pushed) != 1 || tc.pushed[0].UserID != joiner.ID || tc.pushed[0].Qts != st.Qts || st.Qts == 0 {
			t.Fatalf("%s pushes = %+v (state %+v), want one qts update about the joining bot", tc.name, tc.pushed, st)
		}
	}
}
