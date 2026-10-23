package rpc

import (
	"context"
	"strings"
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

// 入群申请的推送接线（#149）：用户经需要审批的链接提交申请后，有邀请权限的管理员 bot 在线
// 收到带 qts 的 updateBotChatInviteRequester（申请人、链接、简介，附带 user 与 chat），离线时
// 从 getDifference 补回；bot 随后以自己的身份批准，申请人入群。
func TestBotChatInviteRequesterPushAndDifference(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	botUpdates := memory.NewBotUpdateStore(users)
	channelStore := memory.NewChannelStore()
	channelStore.AttachBotUpdateStore(botUpdates)
	channels := channelsapp.NewService(channelStore)
	sessions := &phoneCaptureSessions{}
	router := New(Config{PublicBaseURL: "https://links.example"}, Deps{
		Users:      appusers.NewService(users),
		Updates:    appupdates.NewService(memory.NewUpdateStateStore(), memory.NewUpdateEventStore()),
		Channels:   channels,
		BotUpdates: botUpdates,
		Files:      &fakeFiles{},
		Sessions:   sessions,
	}, zaptest.NewLogger(t), clock.System)
	newUser := func(hash int64, phone, name string, bot bool, about string) domain.User {
		t.Helper()
		u, err := users.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name, Bot: bot, About: about})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	owner := newUser(8201, "13800008201", "Owner", false, "")
	bot := newUser(8202, "", "JoinBot", true, "")
	applicant := newUser(8203, "13800008203", "Applicant", false, "hello admins")
	now := int(time.Now().Unix())
	created, err := channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "join requests", Megagroup: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	channelID := created.Channel.ID
	if _, err := channels.InviteToChannel(ctx, owner.ID, channelID, []int64{bot.ID}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := channels.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: bot.ID, AdminRights: domain.ChannelAdminRights{InviteUsers: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	exported, err := channels.ExportInvite(ctx, owner.ID, domain.ExportChannelInviteRequest{ChannelID: channelID, Title: "apply", RequestNeeded: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := botUpdates.BotUpdatesState(ctx, bot.ID)
	botCtx := func(n byte) context.Context {
		return WithAuthKeyID(WithSessionID(WithUserID(ctx, bot.ID), 82020+int64(n)), [8]byte{8, 2, 0, 2, n})
	}
	applicantCtx := WithAuthKeyID(WithSessionID(WithUserID(ctx, applicant.ID), 82030), [8]byte{8, 2, 0, 3})

	if _, err := router.onMessagesImportChatInvite(applicantCtx, exported.Invite.Hash); err == nil || !strings.Contains(err.Error(), "INVITE_REQUEST_SENT") {
		t.Fatalf("import err = %v, want INVITE_REQUEST_SENT", err)
	}

	check := func(where string, update *tg.UpdateBotChatInviteRequester, users []tg.UserClass, chats []tg.ChatClass) {
		t.Helper()
		if update.Qts != before.Qts+1 || update.UserID != applicant.ID || update.About != "hello admins" || update.Date != now {
			t.Fatalf("%s: update = %+v", where, update)
		}
		if peer, ok := update.Peer.(*tg.PeerChannel); !ok || peer.ChannelID != channelID {
			t.Fatalf("%s: peer = %#v", where, update.Peer)
		}
		invite, ok := update.Invite.(*tg.ChatInviteExported)
		if !ok || invite.Link != "https://links.example/+"+exported.Invite.Hash || !invite.RequestNeeded || invite.Title != "apply" {
			t.Fatalf("%s: invite = %#v", where, update.Invite)
		}
		var hasApplicant, hasChannel bool
		for _, u := range users {
			if u, ok := u.(*tg.User); ok && u.ID == applicant.ID {
				hasApplicant = true
			}
		}
		for _, c := range chats {
			if c, ok := c.(*tg.Channel); ok && c.ID == channelID {
				hasChannel = true
			}
		}
		if !hasApplicant || !hasChannel {
			t.Fatalf("%s: users/chats missing applicant=%v channel=%v", where, hasApplicant, hasChannel)
		}
	}

	var pushed []*tg.UpdateBotChatInviteRequester
	for _, rec := range sessions.records() {
		updates, ok := rec.msg.(*tg.Updates)
		if !ok {
			continue
		}
		for _, update := range updates.Updates {
			if u, ok := update.(*tg.UpdateBotChatInviteRequester); ok {
				if rec.userID != bot.ID {
					t.Fatalf("user %d received %+v", rec.userID, u)
				}
				pushed = append(pushed, u)
				check("push", u, updates.Users, updates.Chats)
			}
		}
	}
	if len(pushed) != 1 {
		t.Fatalf("bot received %d updateBotChatInviteRequester, want 1", len(pushed))
	}

	diff, err := router.onUpdatesGetDifference(botCtx(1), &tg.UpdatesGetDifferenceRequest{Qts: before.Qts, Date: 1})
	if err != nil {
		t.Fatalf("getDifference: %v", err)
	}
	full, ok := diff.(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("getDifference = %T", diff)
	}
	var replayed []*tg.UpdateBotChatInviteRequester
	for _, update := range full.OtherUpdates {
		if u, ok := update.(*tg.UpdateBotChatInviteRequester); ok {
			replayed = append(replayed, u)
			check("difference", u, full.Users, full.Chats)
		}
	}
	if len(replayed) != 1 || full.State.Qts != before.Qts+1 {
		t.Fatalf("difference replayed %d requester updates, state qts %d", len(replayed), full.State.Qts)
	}

	if _, err := router.onMessagesHideChatJoinRequest(botCtx(1), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     &tg.InputPeerChannel{ChannelID: channelID, AccessHash: created.Channel.AccessHash},
		UserID:   &tg.InputUser{UserID: applicant.ID, AccessHash: applicant.AccessHash},
	}); err != nil {
		t.Fatalf("bot approves join request: %v", err)
	}
	view, err := channels.GetChannel(ctx, applicant.ID, channelID)
	if err != nil || view.Self.Status != domain.ChannelMemberActive {
		t.Fatalf("applicant after approval = %+v, %v; want an active member", view.Self, err)
	}
}
