package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

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
