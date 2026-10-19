package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// privateGroupWithoutBot 建一个机器人不在其中的私有超级群，返回其 chat_id。
func (f botAPIBridgeFixture) privateGroupWithoutBot(t *testing.T) int64 {
	t.Helper()
	created, err := f.channels.CreateMegagroupFromCreateChat(f.ctx, f.owner.ID, domain.CreateChannelRequest{
		Title:         "Hidden Group",
		MemberUserIDs: []int64{f.member.ID},
		Date:          11,
	})
	if err != nil {
		t.Fatalf("create hidden group: %v", err)
	}
	return -botAPIChannelChatIDBase - created.Channel.ID
}

func TestBotAPIGetChatMemberReturnsMemberAndCreator(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	member, user, _, err := f.router.BotAPIGetChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID)
	if err != nil {
		t.Fatalf("getChatMember(member): %v", err)
	}
	if member.UserID != f.member.ID || member.Role == domain.ChannelRoleCreator || member.Status != domain.ChannelMemberActive ||
		user.ID != f.member.ID || user.Username != "ann_member" {
		t.Fatalf("member = %+v user = %+v", member, user)
	}
	owner, ownerUser, canEdit, err := f.router.BotAPIGetChatMember(f.ctx, f.bot.ID, f.chatID, f.owner.ID)
	if err != nil {
		t.Fatalf("getChatMember(owner): %v", err)
	}
	if owner.Role != domain.ChannelRoleCreator || ownerUser.ID != f.owner.ID || canEdit {
		t.Fatalf("owner = %+v user = %+v canEdit = %v", owner, ownerUser, canEdit)
	}
}

func TestBotAPIGetChatMemberSeesKickedUsers(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	if _, err := f.channels.EditBanned(f.ctx, f.owner.ID, domain.EditChannelBannedRequest{
		ChannelID:    f.channel.ID,
		Participant:  domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID},
		BannedRights: domain.ChannelBannedRights{ViewMessages: true},
		Date:         30,
	}); err != nil {
		t.Fatalf("kick member: %v", err)
	}

	member, _, _, err := f.router.BotAPIGetChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID)
	if err != nil {
		t.Fatalf("getChatMember(kicked): %v", err)
	}
	if member.Status != domain.ChannelMemberKicked || !member.BannedRights.ViewMessages {
		t.Fatalf("member = %+v, want a kicked record", member)
	}
}

// 官方对从未加入的用户返回 status=left，而不是报错。
func TestBotAPIGetChatMemberReportsOutsidersAsLeft(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	outsider, _, err := f.bots.CreateBot(f.ctx, f.owner.ID, "Outsider", "OutsiderBot")
	if err != nil {
		t.Fatalf("create outsider: %v", err)
	}

	member, user, _, err := f.router.BotAPIGetChatMember(f.ctx, f.bot.ID, f.chatID, outsider.ID)
	if err != nil {
		t.Fatalf("getChatMember(outsider): %v", err)
	}
	if member.Status != domain.ChannelMemberLeft || member.UserID != outsider.ID || user.ID != outsider.ID {
		t.Fatalf("member = %+v user = %+v, want a left record", member, user)
	}
	if _, _, _, err := f.router.BotAPIGetChatMember(f.ctx, f.bot.ID, f.chatID, 987654321); err == nil || err.Error() != "USER_ID_INVALID" {
		t.Fatalf("unknown user err = %v, want USER_ID_INVALID", err)
	}
}

func TestBotAPIGetChatAdministratorsListsOwnerAndAdmins(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promote(t, f.owner.ID, f.member.ID, domain.ChannelAdminRights{DeleteMessages: true}, "mod")

	members, users, _, err := f.router.BotAPIGetChatAdministrators(f.ctx, f.bot.ID, f.chatID)
	if err != nil {
		t.Fatalf("getChatAdministrators: %v", err)
	}
	roles := map[int64]domain.ChannelMemberRole{}
	for _, m := range members {
		roles[m.UserID] = m.Role
	}
	if len(members) != 2 || roles[f.owner.ID] != domain.ChannelRoleCreator || roles[f.member.ID] != domain.ChannelRoleAdmin {
		t.Fatalf("admins = %+v, want owner (creator) and member (admin)", members)
	}
	userIDs := map[int64]bool{}
	for _, u := range users {
		userIDs[u.ID] = true
	}
	if !userIDs[f.owner.ID] || !userIDs[f.member.ID] {
		t.Fatalf("users = %+v, want both admins' profiles", users)
	}
}

func TestBotAPIGetChatChannelReturnsTheGroup(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	view, err := f.router.BotAPIGetChatChannel(f.ctx, f.bot.ID, f.chatID)
	if err != nil {
		t.Fatalf("getChat(group): %v", err)
	}
	if view.Channel.ID != f.channel.ID || view.Channel.Title != "Bridge Group" || view.Channel.Username != "bridgegroup" ||
		!view.Channel.Megagroup || view.Self.UserID != f.bot.ID {
		t.Fatalf("view = %+v", view)
	}
	if _, err := f.router.BotAPIGetChatChannel(f.ctx, f.bot.ID, f.privateGroupWithoutBot(t)); err == nil || err.Error() != "CHAT_ID_INVALID" {
		t.Fatalf("getChat(hidden group) err = %v, want CHAT_ID_INVALID", err)
	}
}

func TestBotAPIGetChatUserReturnsTheUser(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	user, err := f.router.BotAPIGetChatUser(f.ctx, f.bot.ID, f.member.ID)
	if err != nil || user.ID != f.member.ID || user.FirstName != "Ann" || user.Username != "ann_member" {
		t.Fatalf("getChat(user) = %+v, %v", user, err)
	}
	if _, err := f.router.BotAPIGetChatUser(f.ctx, f.bot.ID, 987654321); err == nil || err.Error() != "CHAT_ID_INVALID" {
		t.Fatalf("getChat(unknown user) err = %v, want CHAT_ID_INVALID", err)
	}
}

func TestBotAPIChatQueriesRejectGroupsWithoutTheBot(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	hidden := f.privateGroupWithoutBot(t)

	if _, err := f.router.BotAPIGetChatMemberCount(f.ctx, f.bot.ID, hidden); err == nil || err.Error() != "CHAT_ID_INVALID" {
		t.Fatalf("getChatMemberCount err = %v, want CHAT_ID_INVALID", err)
	}
	if _, err := f.router.BotAPIGetChatMemberCount(f.ctx, f.bot.ID, f.member.ID); err == nil {
		t.Fatal("getChatMemberCount accepted a private chat")
	}
}

func TestBotAPIGetChatMemberCountCountsGroupMembers(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	got, err := f.router.BotAPIGetChatMemberCount(f.ctx, f.bot.ID, f.chatID)
	if err != nil || got != 3 {
		t.Fatalf("member count = %d, %v; want 3 (owner, member, bot)", got, err)
	}
}
