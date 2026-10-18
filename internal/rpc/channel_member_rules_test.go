package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// 有 add_admins 的管理员机器人能编辑自己提拔的管理员；失去 add_admins 后不能。
func TestBotAPICanBeEditedFollowsPromoter(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.chatQueryPromote(t, f.owner.ID, f.channel.ID, f.bot.ID, domain.ChannelAdminRights{AddAdmins: true, BanUsers: true, DeleteMessages: true}, "")
	f.promoteMember(t, f.bot.ID, f.channel.ID)

	if _, _, canEdit, err := f.router.BotAPIGetChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID); err != nil || !canEdit {
		t.Fatalf("getChatMember(admin promoted by bot) canEdit = %v, %v; want true", canEdit, err)
	}
	_, _, editable, err := f.router.BotAPIGetChatAdministrators(f.ctx, f.bot.ID, f.chatID)
	if err != nil || !editable[f.member.ID] || editable[f.owner.ID] || editable[f.bot.ID] {
		t.Fatalf("getChatAdministrators editable = %v, %v", editable, err)
	}

	f.chatQueryPromote(t, f.owner.ID, f.channel.ID, f.bot.ID, domain.ChannelAdminRights{BanUsers: true, DeleteMessages: true}, "")
	if _, _, canEdit, err := f.router.BotAPIGetChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID); err != nil || canEdit {
		t.Fatalf("getChatMember without add_admins canEdit = %v, %v; want false", canEdit, err)
	}
}

// chat_member 载荷带上收件机器人自己的成员状态，投影据此算 can_be_edited。
func TestBotAPIChatMemberPayloadCarriesRecipientViewer(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.chatQueryPromote(t, f.owner.ID, f.channel.ID, f.bot.ID, domain.ChannelAdminRights{AddAdmins: true, BanUsers: true, DeleteMessages: true}, "")
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := f.router.BotAPIPromoteChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, domain.ChannelAdminRights{DeleteMessages: true}); err != nil {
		t.Fatalf("bot promotes member: %v", err)
	}
	events := botMemberEvents(t, f, domain.UpdateEventBotChatMember)
	if len(events) != 1 {
		t.Fatalf("chat_member events = %v", memberTransitions(events))
	}
	update := events[0].BotChatMember
	if update.Viewer.UserID != f.bot.ID || update.Viewer.Role != domain.ChannelRoleAdmin || !update.Viewer.AdminRights.AddAdmins ||
		update.New.InviterUserID != f.bot.ID {
		t.Fatalf("payload viewer = %+v new = %+v", update.Viewer, update.New)
	}
	if !domain.CanEditChannelAdmin(update.Viewer.ChannelMember(f.channel.ID), update.New.ChannelMember(f.channel.ID)) {
		t.Fatal("bot cannot edit the admin it promoted")
	}
}

// 非群主管理员不能封禁/限制/改动别人提拔的管理员：MTProto 与 Bot API 都返回 USER_ADMIN_INVALID。
func TestChannelAdminProtectionMapsUserAdminInvalid(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true, AddAdmins: true, PinMessages: true})
	if _, err := f.channels.EditAdmin(f.ctx, f.owner.ID, domain.EditChannelAdminRequest{
		UserID: f.owner.ID, ChannelID: f.channel.ID, MemberID: f.member.ID,
		AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: 21,
	}); err != nil {
		t.Fatalf("promote member: %v", err)
	}

	if err := f.router.BotAPIRestrictChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, domain.ChannelBannedRights{SendMessages: true}); !tgerr.Is(err, "USER_ADMIN_INVALID") {
		t.Fatalf("bot restricts foreign admin = %v, want USER_ADMIN_INVALID", err)
	}
	if err := f.router.BotAPIBanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, 0, false); !tgerr.Is(err, "USER_ADMIN_INVALID") {
		t.Fatalf("bot bans foreign admin = %v, want USER_ADMIN_INVALID", err)
	}
	if err := f.router.BotAPIPromoteChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, domain.ChannelAdminRights{}); !tgerr.Is(err, "USER_ADMIN_INVALID") {
		t.Fatalf("bot demotes foreign admin = %v, want USER_ADMIN_INVALID", err)
	}

	_, err := f.router.onChannelsEditBanned(WithUserID(f.ctx, f.member.ID), &tg.ChannelsEditBannedRequest{
		Channel:      &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		Participant:  &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		BannedRights: tg.ChatBannedRights{ViewMessages: true},
	})
	if !tgerr.Is(err, "USER_ADMIN_INVALID") {
		t.Fatalf("admin kicks foreign admin = %v, want USER_ADMIN_INVALID", err)
	}
	member, err := f.channels.GetParticipant(f.ctx, f.owner.ID, f.channel.ID, f.member.ID)
	if err != nil || member.Role != domain.ChannelRoleAdmin || member.Status != domain.ChannelMemberActive {
		t.Fatalf("foreign admin = %+v, %v; want untouched admin", member, err)
	}
}
