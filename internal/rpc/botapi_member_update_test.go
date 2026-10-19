package rpc

import (
	"slices"
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// botMemberEvents 取出机器人队列里指定类型的成员更新。
func botMemberEvents(t *testing.T, f botAPIBridgeFixture, eventType domain.UpdateEventType) []domain.UpdateEvent {
	t.Helper()
	events, err := f.router.BotAPIUpdates(f.ctx, f.bot.ID, 0)
	if err != nil {
		t.Fatalf("BotAPIUpdates: %v", err)
	}
	var out []domain.UpdateEvent
	for _, event := range events {
		if event.Type == eventType {
			if event.BotChatMember == nil {
				t.Fatalf("%s event without payload: %+v", eventType, event)
			}
			out = append(out, event)
		}
	}
	return out
}

func (f botAPIBridgeFixture) inputChannel() *tg.InputChannel {
	return &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
}

func TestBotAPIMemberUpdateMyChatMemberOnPromotion(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	f.promote(t, f.owner.ID, f.bot.ID, domain.ChannelAdminRights{BanUsers: true, DeleteMessages: true}, "guard")

	events := botMemberEvents(t, f, domain.UpdateEventBotMyChatMember)
	if len(events) != 1 {
		t.Fatalf("my_chat_member events = %+v, want one", events)
	}
	event := events[0]
	update := event.BotChatMember
	if event.Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}) || event.BotAPIUpdateID <= 0 ||
		update.ActorUserID != f.owner.ID || update.Chat != event.Peer || update.Date <= 0 ||
		update.Old.UserID != f.bot.ID || update.Old.Role != domain.ChannelRoleMember ||
		update.New.Role != domain.ChannelRoleAdmin || !update.New.AdminRights.BanUsers || update.New.Rank != "guard" {
		t.Fatalf("my_chat_member = %+v payload %+v", event, update)
	}
	// 投影需要的用户和群必须随事件带上。
	if !hasUser(event.Users, f.owner.ID) || !hasUser(event.Users, f.bot.ID) || len(event.Channels) == 0 {
		t.Fatalf("event users %+v channels %+v", event.Users, event.Channels)
	}
	if got := botMemberEvents(t, f, domain.UpdateEventBotChatMember); len(got) != 0 {
		t.Fatalf("the bot's own change must not also arrive as chat_member: %+v", got)
	}
}

func (f botAPIBridgeFixture) restrictMember(t *testing.T, rights tg.ChatBannedRights) {
	t.Helper()
	if _, err := f.router.onChannelsEditBanned(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsEditBannedRequest{
		Channel:      f.inputChannel(),
		Participant:  &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash},
		BannedRights: rights,
	}); err != nil {
		t.Fatalf("edit banned: %v", err)
	}
}

func TestBotAPIMemberUpdateChatMemberNeedsAdminBotAndOptIn(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// 订阅了但机器人只是普通成员：不投递。
	f.restrictMember(t, tg.ChatBannedRights{SendMedia: true})
	if got := botMemberEvents(t, f, domain.UpdateEventBotChatMember); len(got) != 0 {
		t.Fatalf("non-admin bot got chat_member: %+v", got)
	}

	// 是管理员但没订阅（默认 allowed_updates）：不投递。
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, nil); err != nil {
		t.Fatalf("reset allowed updates: %v", err)
	}
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true, InviteUsers: true})
	f.restrictMember(t, tg.ChatBannedRights{SendMedia: true, SendPolls: true})
	if got := botMemberEvents(t, f, domain.UpdateEventBotChatMember); len(got) != 0 {
		t.Fatalf("unsubscribed admin bot got chat_member: %+v", got)
	}

	// 管理员 + 显式订阅：踢人收到 chat_member。
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	f.restrictMember(t, tg.ChatBannedRights{ViewMessages: true})
	got := botMemberEvents(t, f, domain.UpdateEventBotChatMember)
	if len(got) != 1 {
		t.Fatalf("chat_member events = %+v, want one", got)
	}
	update := got[0].BotChatMember
	if update.ActorUserID != f.owner.ID || update.New.UserID != f.member.ID ||
		!update.Old.BannedRights.SendPolls || update.New.Status != domain.ChannelMemberKicked {
		t.Fatalf("chat_member payload = %+v", update)
	}

	// 普通用户永远不入队。
	rows, err := f.router.deps.BotAPIUpdates.ListBotAPIUpdates(f.ctx, f.member.ID, 1, 100)
	if err != nil || len(rows) != 0 {
		t.Fatalf("non-bot queue = %+v, %v", rows, err)
	}
}

// memberTransitions 把 chat_member / my_chat_member 事件压成 "old->new@actor" 便于比对。
func memberTransitions(events []domain.UpdateEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		u := event.BotChatMember
		out = append(out, string(u.Old.Status)+"/"+string(u.Old.Role)+"->"+string(u.New.Status)+"/"+string(u.New.Role)+"@"+itoa64(u.ActorUserID))
	}
	return out
}

func itoa64(v int64) string {
	return strconv.FormatInt(v, 10)
}

// exportInvite 由群主导出一条测试群的邀请链接，返回其 hash。
func (f botAPIBridgeFixture) exportInvite(t *testing.T, requestNeeded bool) string {
	t.Helper()
	res, err := f.channels.ExportInvite(f.ctx, f.owner.ID, domain.ExportChannelInviteRequest{
		UserID: f.owner.ID, ChannelID: f.channel.ID, RequestNeeded: requestNeeded, Date: 30,
	})
	if err != nil {
		t.Fatalf("export invite: %v", err)
	}
	return res.Invite.Hash
}

// 成员先退群（两种退群请求），再经各条入群路径回来：每一步都给订阅了的管理员机器人
// 产生一条 chat_member，actor 与 via_join_request 对应实际入群方式。
func TestBotAPIMemberUpdateJoinPathsProduceChatMember(t *testing.T) {
	leaveChannel := func(t *testing.T, f botAPIBridgeFixture) {
		t.Helper()
		if _, err := f.router.onChannelsLeaveChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel()); err != nil {
			t.Fatalf("leave: %v", err)
		}
	}
	for _, tc := range []struct {
		name           string
		leave          func(t *testing.T, f botAPIBridgeFixture)
		join           func(t *testing.T, f botAPIBridgeFixture)
		ownerActs      bool
		viaJoinRequest bool
	}{
		{
			name:  "joinChannel",
			leave: leaveChannel,
			join: func(t *testing.T, f botAPIBridgeFixture) {
				if _, err := f.router.onChannelsJoinChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel()); err != nil {
					t.Fatalf("join: %v", err)
				}
			},
		},
		{
			name: "legacy deleteChatUser leave then invite",
			leave: func(t *testing.T, f botAPIBridgeFixture) {
				if _, err := f.router.onMessagesDeleteChatUser(WithUserID(f.ctx, f.member.ID), &tg.MessagesDeleteChatUserRequest{
					ChatID: f.channel.ID, UserID: &tg.InputUserSelf{},
				}); err != nil {
					t.Fatalf("legacy leave: %v", err)
				}
			},
			join: func(t *testing.T, f botAPIBridgeFixture) {
				if _, err := f.router.onChannelsInviteToChannel(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsInviteToChannelRequest{
					Channel: f.inputChannel(),
					Users:   []tg.InputUserClass{&tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}},
				}); err != nil {
					t.Fatalf("invite: %v", err)
				}
			},
			ownerActs: true,
		},
		{
			name:  "invite link",
			leave: leaveChannel,
			join: func(t *testing.T, f botAPIBridgeFixture) {
				if _, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), f.exportInvite(t, false)); err != nil {
					t.Fatalf("import invite: %v", err)
				}
			},
		},
		{
			name:  "approved join request",
			leave: leaveChannel,
			join: func(t *testing.T, f botAPIBridgeFixture) {
				if _, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), f.exportInvite(t, true)); err == nil {
					t.Fatal("request-needed invite joined directly")
				}
				if _, err := f.router.onMessagesHideChatJoinRequest(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideChatJoinRequestRequest{
					Approved: true,
					Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
					UserID:   &tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash},
				}); err != nil {
					t.Fatalf("approve join request: %v", err)
				}
			},
			ownerActs:      true,
			viaJoinRequest: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true, InviteUsers: true})
			if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			tc.leave(t, f)
			tc.join(t, f)

			member, joiner := itoa64(f.member.ID), itoa64(f.member.ID)
			if tc.ownerActs {
				joiner = itoa64(f.owner.ID)
			}
			want := []string{
				"active/member->left/member@" + member,
				"left/member->active/member@" + joiner,
			}
			events := botMemberEvents(t, f, domain.UpdateEventBotChatMember)
			if got := memberTransitions(events); !slices.Equal(got, want) {
				t.Fatalf("chat_member transitions = %v, want %v", got, want)
			}
			if got := events[1].BotChatMember.ViaJoinRequest; got != tc.viaJoinRequest {
				t.Fatalf("via_join_request = %v, want %v", got, tc.viaJoinRequest)
			}
		})
	}
}

func TestBotAPIMemberUpdateBotKickedReinvitedAndLeaving(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	ownerCtx := WithUserID(f.ctx, f.owner.ID)
	if _, err := f.router.onChannelsEditBanned(ownerCtx, &tg.ChannelsEditBannedRequest{
		Channel:      f.inputChannel(),
		Participant:  &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		BannedRights: tg.ChatBannedRights{ViewMessages: true},
	}); err != nil {
		t.Fatalf("kick bot: %v", err)
	}
	if _, err := f.router.onMessagesAddChatUser(ownerCtx, &tg.MessagesAddChatUserRequest{
		ChatID: f.channel.ID, UserID: &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
	}); err != nil {
		t.Fatalf("re-add bot: %v", err)
	}
	if _, err := f.router.onChannelsLeaveChannel(WithUserID(f.ctx, f.bot.ID), f.inputChannel()); err != nil {
		t.Fatalf("bot leaves: %v", err)
	}
	bot, owner := itoa64(f.bot.ID), itoa64(f.owner.ID)
	want := []string{
		"active/member->kicked/member@" + owner,
		// 被踢后重新拉入：旧状态是真实的 kicked（对齐官方）。
		"kicked/member->active/member@" + owner,
		"active/member->left/member@" + bot,
	}
	if got := memberTransitions(botMemberEvents(t, f, domain.UpdateEventBotMyChatMember)); !slices.Equal(got, want) {
		t.Fatalf("my_chat_member transitions = %v, want %v", got, want)
	}
}

func TestBotAPIMemberUpdateMyChatMemberOnCreateChat(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	if _, err := f.router.onMessagesCreateChat(WithUserID(f.ctx, f.owner.ID), &tg.MessagesCreateChatRequest{
		Title: "Fresh Group",
		Users: []tg.InputUserClass{&tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}},
	}); err != nil {
		t.Fatalf("create chat: %v", err)
	}
	events := botMemberEvents(t, f, domain.UpdateEventBotMyChatMember)
	if len(events) != 1 || events[0].Peer.ID == f.channel.ID || events[0].BotChatMember.New.Status != domain.ChannelMemberActive ||
		events[0].BotChatMember.ActorUserID != f.owner.ID {
		t.Fatalf("my_chat_member after createChat = %v", memberTransitions(events))
	}
}

func TestBotAPIMemberUpdateMyChatMemberOnRankEdit(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true, InviteUsers: true})
	if _, err := f.router.onMessagesEditChatParticipantRank(WithUserID(f.ctx, f.owner.ID), &tg.MessagesEditChatParticipantRankRequest{
		Peer:        &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		Participant: &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		Rank:        "night shift",
	}); err != nil {
		t.Fatalf("edit rank: %v", err)
	}
	events := botMemberEvents(t, f, domain.UpdateEventBotMyChatMember)
	if len(events) != 2 {
		t.Fatalf("my_chat_member events = %v, want promotion and rank edit", memberTransitions(events))
	}
	last := events[1].BotChatMember
	if last.Old.Rank != "" || last.New.Rank != "night shift" || last.New.Role != domain.ChannelRoleAdmin {
		t.Fatalf("my_chat_member after rank edit = %+v", last)
	}
}

func hasUser(users []domain.User, id int64) bool {
	for _, user := range users {
		if user.ID == id {
			return true
		}
	}
	return false
}
