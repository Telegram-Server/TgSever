package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// 有 add_admins 的管理员机器人提拔的管理员，getChatMember 对它报 can_be_edited=true。
func TestBotAPICanBeEditedFollowsPromoter(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{AddAdmins: true, BanUsers: true, DeleteMessages: true})
	f.promote(t, f.bot.ID, f.member.ID, domain.ChannelAdminRights{DeleteMessages: true}, "mod")

	if _, _, canEdit, err := f.router.BotAPIGetChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID); err != nil || !canEdit {
		t.Fatalf("getChatMember(admin promoted by bot) canEdit = %v, %v; want true", canEdit, err)
	}
}

// 群主退群时接任的机器人收到 my_chat_member，旧状态是它真实的管理员身份。
func TestBotAPIMemberUpdateUsesRealPreviousState(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true, InviteUsers: true})
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateMyChatMember}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := f.router.onChannelsLeaveChannel(WithUserID(f.ctx, f.owner.ID), f.inputChannel()); err != nil {
		t.Fatalf("owner leaves: %v", err)
	}
	mine := botMemberEvents(t, f, domain.UpdateEventBotMyChatMember)
	if len(mine) == 0 {
		t.Fatal("successor bot got no my_chat_member")
	}
	last := mine[len(mine)-1].BotChatMember
	if last.Old.Role != domain.ChannelRoleAdmin || !last.Old.AdminRights.BanUsers || last.New.Role != domain.ChannelRoleCreator {
		t.Fatalf("successor my_chat_member old = %+v new = %+v", last.Old, last.New)
	}
}

// chat_member 载荷带上收件机器人自己的成员状态，投影据此算 can_be_edited。
func TestBotAPIChatMemberPayloadCarriesRecipientViewer(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{AddAdmins: true, BanUsers: true, DeleteMessages: true})
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
}

// 非群主管理员不能封禁别人提拔的管理员：Bot API 与 MTProto 都返回 USER_ADMIN_INVALID。
func TestChannelAdminProtectionMapsUserAdminInvalid(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true, AddAdmins: true, PinMessages: true})
	f.promote(t, f.owner.ID, f.member.ID, domain.ChannelAdminRights{BanUsers: true}, "")

	if err := f.router.BotAPIBanChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, 0, false); !tgerr.Is(err, "USER_ADMIN_INVALID") {
		t.Fatalf("bot bans foreign admin = %v, want USER_ADMIN_INVALID", err)
	}
	// 降级走的是 channels.editAdmin，与封禁不是同一条处理器路径。
	if err := f.router.BotAPIPromoteChatMember(f.ctx, f.bot.ID, f.chatID, f.member.ID, domain.ChannelAdminRights{}); !tgerr.Is(err, "USER_ADMIN_INVALID") {
		t.Fatalf("bot demotes foreign admin = %v, want USER_ADMIN_INVALID", err)
	}
	_, err := f.router.onChannelsEditBanned(WithUserID(f.ctx, f.member.ID), &tg.ChannelsEditBannedRequest{
		Channel:      f.inputChannel(),
		Participant:  &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		BannedRights: tg.ChatBannedRights{ViewMessages: true},
	})
	if !tgerr.Is(err, "USER_ADMIN_INVALID") {
		t.Fatalf("admin kicks foreign admin = %v, want USER_ADMIN_INVALID", err)
	}
}
