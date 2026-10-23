package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// 非群主管理员不能封禁别人提拔的管理员：返回 USER_ADMIN_INVALID。
func TestChannelAdminProtectionMapsUserAdminInvalid(t *testing.T) {
	f := newBotGroupFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true, AddAdmins: true, PinMessages: true})
	f.promote(t, f.owner.ID, f.member.ID, domain.ChannelAdminRights{BanUsers: true}, "")

	_, err := f.router.onChannelsEditBanned(WithUserID(f.ctx, f.member.ID), &tg.ChannelsEditBannedRequest{
		Channel:      f.inputChannel(),
		Participant:  &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
		BannedRights: tg.ChatBannedRights{ViewMessages: true},
	})
	if !tgerr.Is(err, "USER_ADMIN_INVALID") {
		t.Fatalf("admin kicks foreign admin = %v, want USER_ADMIN_INVALID", err)
	}
}
