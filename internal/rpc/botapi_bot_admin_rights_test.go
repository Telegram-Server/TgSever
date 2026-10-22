package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// Bot API set/getMyDefaultAdministratorRights 桥接：读回设置的值、未设置为全 false、
// 设置相同值仍算成功、零值清空，且写入经 MTProto 处理器后到达其他用户的 userFull。
func TestBotAPIDefaultAdministratorRightsRoundTrip(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	group := domain.ChannelAdminRights{DeleteMessages: true, InviteUsers: true, ManageChat: true}
	broadcast := domain.ChannelAdminRights{PostMessages: true}

	for _, forChannels := range []bool{false, true} {
		got, err := f.router.BotAPIGetMyDefaultAdministratorRights(f.ctx, f.bot.ID, forChannels)
		if err != nil || got != (domain.ChannelAdminRights{}) {
			t.Fatalf("unset rights (channels=%v) = %+v, %v; want all false", forChannels, got, err)
		}
	}

	if err := f.router.BotAPISetMyDefaultAdministratorRights(f.ctx, f.bot.ID, false, group); err != nil {
		t.Fatalf("set group: %v", err)
	}
	if err := f.router.BotAPISetMyDefaultAdministratorRights(f.ctx, f.bot.ID, true, broadcast); err != nil {
		t.Fatalf("set broadcast: %v", err)
	}
	// 相同的值：MTProto 层是 RIGHTS_NOT_MODIFIED，Bot API 层算成功。
	if err := f.router.BotAPISetMyDefaultAdministratorRights(f.ctx, f.bot.ID, false, group); err != nil {
		t.Fatalf("set same group rights: %v, want success", err)
	}
	if got, err := f.router.BotAPIGetMyDefaultAdministratorRights(f.ctx, f.bot.ID, false); err != nil || got != group {
		t.Fatalf("group rights = %+v, %v; want %+v", got, err, group)
	}
	if got, err := f.router.BotAPIGetMyDefaultAdministratorRights(f.ctx, f.bot.ID, true); err != nil || got != broadcast {
		t.Fatalf("broadcast rights = %+v, %v; want %+v", got, err, broadcast)
	}
	full := f.botFullUser(t, f.member.ID)
	if rights, ok := full.GetBotGroupAdminRights(); !ok || !rights.DeleteMessages || !rights.InviteUsers || !rights.Other {
		t.Fatalf("member sees bot_group_admin_rights = %+v (set=%v)", rights, ok)
	}

	if err := f.router.BotAPISetMyDefaultAdministratorRights(f.ctx, f.bot.ID, false, domain.ChannelAdminRights{}); err != nil {
		t.Fatalf("clear group: %v", err)
	}
	if got, err := f.router.BotAPIGetMyDefaultAdministratorRights(f.ctx, f.bot.ID, false); err != nil || got != (domain.ChannelAdminRights{}) {
		t.Fatalf("group rights after clear = %+v, %v", got, err)
	}
}
