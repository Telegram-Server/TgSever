package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func TestBotDefaultAdminRightsRPC(t *testing.T) {
	f := newInlineBotRPCTestFixture(t)
	ctx := context.Background()
	botCtx := WithUserID(ctx, f.bot.ID)
	group := tg.ChatAdminRights{DeleteMessages: true, BanUsers: true, InviteUsers: true}
	broadcast := tg.ChatAdminRights{PostMessages: true, EditMessages: true}
	for _, viewer := range []int64{f.owner.ID, f.peer.ID} {
		if _, err := f.router.onUsersGetFullUser(WithUserID(ctx, viewer), inputUser(f.bot)); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("group defaults are visible immediately to every viewer", func(t *testing.T) {
		if ok, err := f.router.onBotsSetBotGroupDefaultAdminRights(botCtx, group); err != nil || !ok {
			t.Fatalf("set group = %v, %v", ok, err)
		}
		for _, viewer := range []int64{f.owner.ID, f.peer.ID} {
			full, err := f.router.onUsersGetFullUser(WithUserID(ctx, viewer), inputUser(f.bot))
			if err != nil {
				t.Fatal(err)
			}
			got, ok := full.FullUser.GetBotGroupAdminRights()
			if !ok || !got.DeleteMessages || !got.BanUsers || !got.InviteUsers || got.PostMessages {
				t.Fatalf("group rights = %+v, present=%v", got, ok)
			}
		}
	})
	t.Run("broadcast defaults are independent", func(t *testing.T) {
		if ok, err := f.router.onBotsSetBotBroadcastDefaultAdminRights(botCtx, broadcast); err != nil || !ok {
			t.Fatalf("set broadcast = %v, %v", ok, err)
		}
		full, err := f.router.onUsersGetFullUser(WithUserID(ctx, f.peer.ID), inputUser(f.bot))
		if err != nil {
			t.Fatal(err)
		}
		got, ok := full.FullUser.GetBotBroadcastAdminRights()
		if !ok || !got.PostMessages || !got.EditMessages || got.BanUsers {
			t.Fatalf("broadcast rights = %+v present=%v", got, ok)
		}
		got, ok = full.FullUser.GetBotGroupAdminRights()
		if !ok || !got.BanUsers {
			t.Fatalf("group rights lost: %+v", got)
		}
	})
	t.Run("internal gateway reader returns each scope", func(t *testing.T) {
		groupRights, err := f.router.BotAPIGetMyDefaultAdministratorRights(ctx, f.bot.ID, false)
		if err != nil || groupRights != domainChannelAdminRights(group) {
			t.Fatalf("read group rights = %+v,%v", groupRights, err)
		}
		broadcastRights, err := f.router.BotAPIGetMyDefaultAdministratorRights(ctx, f.bot.ID, true)
		if err != nil || broadcastRights != domainChannelAdminRights(broadcast) {
			t.Fatalf("read broadcast rights = %+v,%v", broadcastRights, err)
		}
		if _, err := f.router.BotAPIGetMyDefaultAdministratorRights(ctx, f.owner.ID, false); err == nil {
			t.Fatal("non-bot internal reader succeeded")
		}
	})
	t.Run("unchanged values are rejected", func(t *testing.T) {
		if _, err := f.router.onBotsSetBotGroupDefaultAdminRights(botCtx, group); !tgerr.Is(err, "RIGHTS_NOT_MODIFIED") {
			t.Fatalf("same group rights: %v", err)
		}
		if _, err := f.router.onBotsSetBotBroadcastDefaultAdminRights(botCtx, broadcast); !tgerr.Is(err, "RIGHTS_NOT_MODIFIED") {
			t.Fatalf("same broadcast rights: %v", err)
		}
	})
	t.Run("ordinary users cannot set defaults", func(t *testing.T) {
		if _, err := f.router.onBotsSetBotGroupDefaultAdminRights(WithUserID(ctx, f.owner.ID), group); !tgerr.Is(err, "USER_BOT_REQUIRED") {
			t.Fatalf("non-bot group set: %v", err)
		}
		if _, err := f.router.onBotsSetBotBroadcastDefaultAdminRights(WithUserID(ctx, f.owner.ID), broadcast); !tgerr.Is(err, "USER_BOT_REQUIRED") {
			t.Fatalf("non-bot broadcast set: %v", err)
		}
	})
}
