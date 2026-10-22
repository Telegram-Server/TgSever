package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (f botAPIBridgeFixture) botFullUser(t *testing.T, viewer int64) *tg.UserFull {
	t.Helper()
	full, err := f.router.onUsersGetFullUser(WithUserID(f.ctx, viewer), &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash})
	if err != nil {
		t.Fatalf("getFullUser(bot) as %d: %v", viewer, err)
	}
	return &full.FullUser
}

// bots.setBotGroup/BroadcastDefaultAdminRights 的协议映射：bot 自己写入后，其他用户的
// userFull（即使之前已被缓存）立刻带上 bot_group_admin_rights / bot_broadcast_admin_rights。
func TestBotsSetDefaultAdminRightsReachesUserFull(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	fresh := f.botFullUser(t, f.member.ID)
	if _, ok := fresh.GetBotGroupAdminRights(); ok {
		t.Fatal("fresh bot userFull already carries bot_group_admin_rights")
	}
	if _, ok := fresh.GetBotBroadcastAdminRights(); ok {
		t.Fatal("fresh bot userFull already carries bot_broadcast_admin_rights")
	}
	botCtx := WithUserID(f.ctx, f.bot.ID)
	group := tg.ChatAdminRights{DeleteMessages: true, BanUsers: true, InviteUsers: true}
	broadcast := tg.ChatAdminRights{PostMessages: true, EditMessages: true}
	if ok, err := f.router.onBotsSetBotGroupDefaultAdminRights(botCtx, group); err != nil || !ok {
		t.Fatalf("set group rights = %v, %v", ok, err)
	}
	if ok, err := f.router.onBotsSetBotBroadcastDefaultAdminRights(botCtx, broadcast); err != nil || !ok {
		t.Fatalf("set broadcast rights = %v, %v", ok, err)
	}

	full := f.botFullUser(t, f.member.ID)
	gotGroup, ok := full.GetBotGroupAdminRights()
	if !ok || !gotGroup.DeleteMessages || !gotGroup.BanUsers || !gotGroup.InviteUsers || gotGroup.PostMessages {
		t.Fatalf("bot_group_admin_rights = %+v (set=%v), want delete+ban+invite", gotGroup, ok)
	}
	gotBroadcast, ok := full.GetBotBroadcastAdminRights()
	if !ok || !gotBroadcast.PostMessages || !gotBroadcast.EditMessages || gotBroadcast.BanUsers {
		t.Fatalf("bot_broadcast_admin_rights = %+v (set=%v), want post+edit", gotBroadcast, ok)
	}

	// 清空后从 userFull 消失。
	if _, err := f.router.onBotsSetBotGroupDefaultAdminRights(botCtx, tg.ChatAdminRights{}); err != nil {
		t.Fatalf("clear group rights: %v", err)
	}
	if _, ok := f.botFullUser(t, f.owner.ID).GetBotGroupAdminRights(); ok {
		t.Fatal("cleared group rights still in userFull")
	}
}

func TestBotsSetDefaultAdminRightsErrors(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	rights := tg.ChatAdminRights{PinMessages: true}

	if _, err := f.router.onBotsSetBotGroupDefaultAdminRights(WithUserID(f.ctx, f.member.ID), rights); err == nil {
		t.Fatal("human set group default rights succeeded")
	} else {
		wantBotAPIError(t, err, "USER_BOT_REQUIRED")
	}
	if _, err := f.router.onBotsSetBotBroadcastDefaultAdminRights(WithUserID(f.ctx, f.member.ID), rights); err == nil {
		t.Fatal("human set broadcast default rights succeeded")
	} else {
		wantBotAPIError(t, err, "USER_BOT_REQUIRED")
	}

	botCtx := WithUserID(f.ctx, f.bot.ID)
	if _, err := f.router.onBotsSetBotGroupDefaultAdminRights(botCtx, rights); err != nil {
		t.Fatalf("first set: %v", err)
	}
	_, err := f.router.onBotsSetBotGroupDefaultAdminRights(botCtx, rights)
	wantBotAPIError(t, err, "RIGHTS_NOT_MODIFIED")
	_, err = f.router.onBotsSetBotBroadcastDefaultAdminRights(botCtx, tg.ChatAdminRights{})
	wantBotAPIError(t, err, "RIGHTS_NOT_MODIFIED")

	profile, _, _ := f.bots.BotInfo(f.ctx, f.bot.ID)
	if profile.GroupAdminRights != (domain.ChannelAdminRights{PinMessages: true}) {
		t.Fatalf("stored group rights = %+v", profile.GroupAdminRights)
	}
}
