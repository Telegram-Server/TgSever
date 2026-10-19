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

func (f botAPIBridgeFixture) asOwner() botAPIBridgeFixture {
	f.ctx = WithUserID(f.ctx, f.owner.ID)
	return f
}

func (f botAPIBridgeFixture) inputChannel() *tg.InputChannel {
	return &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
}

func TestBotAPIMemberUpdateMyChatMemberOnPromotion(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	if _, err := f.router.onChannelsEditAdmin(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsEditAdminRequest{
		Channel:     f.inputChannel(),
		UserID:      &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		AdminRights: tg.ChatAdminRights{BanUsers: true, DeleteMessages: true},
		Rank:        "guard",
	}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}

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

// promoteBotViaRPC 走 channels.editAdmin 提拔机器人（fixture 的 promoteBot 没填
// MemberID，在 store 层会报 channel invalid）。
func (f botAPIBridgeFixture) promoteBotViaRPC(t *testing.T) {
	t.Helper()
	if _, err := f.router.onChannelsEditAdmin(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsEditAdminRequest{
		Channel:     f.inputChannel(),
		UserID:      &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		AdminRights: tg.ChatAdminRights{BanUsers: true, InviteUsers: true},
	}); err != nil {
		t.Fatalf("promote bot: %v", err)
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
	f.promoteBotViaRPC(t)
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

func TestBotAPIMemberUpdateJoinLeaveInvitePaths(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBotViaRPC(t)
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	memberCtx := WithUserID(f.ctx, f.member.ID)
	if _, err := f.router.onChannelsLeaveChannel(memberCtx, f.inputChannel()); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := f.router.onChannelsJoinChannel(memberCtx, f.inputChannel()); err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := f.router.onMessagesDeleteChatUser(memberCtx, &tg.MessagesDeleteChatUserRequest{
		ChatID: f.channel.ID, UserID: &tg.InputUserSelf{},
	}); err != nil {
		t.Fatalf("legacy leave: %v", err)
	}
	if _, err := f.router.onChannelsInviteToChannel(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsInviteToChannelRequest{
		Channel: f.inputChannel(),
		Users:   []tg.InputUserClass{&tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}},
	}); err != nil {
		t.Fatalf("invite: %v", err)
	}
	member, owner := itoa64(f.member.ID), itoa64(f.owner.ID)
	want := []string{
		"active/member->left/member@" + member,
		"left/member->active/member@" + member,
		"active/member->left/member@" + member,
		"left/member->active/member@" + owner,
	}
	if got := memberTransitions(botMemberEvents(t, f, domain.UpdateEventBotChatMember)); !slices.Equal(got, want) {
		t.Fatalf("chat_member transitions = %v, want %v", got, want)
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

func TestBotAPIMemberUpdateInviteLinkAndJoinRequest(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBotViaRPC(t)
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	memberCtx := WithUserID(f.ctx, f.member.ID)
	export := func(requestNeeded bool) string {
		res, err := f.channels.ExportInvite(f.ctx, f.owner.ID, domain.ExportChannelInviteRequest{
			UserID: f.owner.ID, ChannelID: f.channel.ID, RequestNeeded: requestNeeded, Date: 30,
		})
		if err != nil {
			t.Fatalf("export invite: %v", err)
		}
		return res.Invite.Hash
	}
	if _, err := f.router.onChannelsLeaveChannel(memberCtx, f.inputChannel()); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := f.router.onMessagesImportChatInvite(memberCtx, export(false)); err != nil {
		t.Fatalf("import invite: %v", err)
	}
	if _, err := f.router.onChannelsLeaveChannel(memberCtx, f.inputChannel()); err != nil {
		t.Fatalf("leave again: %v", err)
	}
	if _, err := f.router.onMessagesImportChatInvite(memberCtx, export(true)); err == nil {
		t.Fatal("request-needed invite joined directly")
	}
	if _, err := f.router.onMessagesHideChatJoinRequest(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: true,
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		UserID:   &tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash},
	}); err != nil {
		t.Fatalf("approve join request: %v", err)
	}
	events := botMemberEvents(t, f, domain.UpdateEventBotChatMember)
	member, owner := itoa64(f.member.ID), itoa64(f.owner.ID)
	want := []string{
		"active/member->left/member@" + member,
		"left/member->active/member@" + member,
		"active/member->left/member@" + member,
		"left/member->active/member@" + owner,
	}
	if got := memberTransitions(events); !slices.Equal(got, want) {
		t.Fatalf("chat_member transitions = %v, want %v", got, want)
	}
	if events[1].BotChatMember.ViaJoinRequest || !events[3].BotChatMember.ViaJoinRequest {
		t.Fatalf("via_join_request flags = %v / %v", events[1].BotChatMember.ViaJoinRequest, events[3].BotChatMember.ViaJoinRequest)
	}
}

func TestBotAPIMemberUpdateCreateChatWithBotAndRankEdit(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	ownerCtx := WithUserID(f.ctx, f.owner.ID)
	created, err := f.router.onMessagesCreateChat(ownerCtx, &tg.MessagesCreateChatRequest{
		Title: "Fresh Group",
		Users: []tg.InputUserClass{&tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}},
	})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	_ = created
	events := botMemberEvents(t, f, domain.UpdateEventBotMyChatMember)
	if len(events) != 1 || events[0].Peer.ID == f.channel.ID || events[0].BotChatMember.New.Status != domain.ChannelMemberActive ||
		events[0].BotChatMember.ActorUserID != f.owner.ID {
		t.Fatalf("my_chat_member after createChat = %v", memberTransitions(events))
	}

	f.promoteBotViaRPC(t)
	if _, err := f.router.onMessagesEditChatParticipantRank(ownerCtx, &tg.MessagesEditChatParticipantRankRequest{
		Peer:        &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		Participant: &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		Rank:        "night shift",
	}); err != nil {
		t.Fatalf("edit rank: %v", err)
	}
	events = botMemberEvents(t, f, domain.UpdateEventBotMyChatMember)
	last := events[len(events)-1].BotChatMember
	if len(events) != 3 || last.Old.Rank != "" || last.New.Rank != "night shift" || last.New.Role != domain.ChannelRoleAdmin {
		t.Fatalf("my_chat_member after rank edit = %v, last %+v", memberTransitions(events), last)
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
