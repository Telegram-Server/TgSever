package bots

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// 默认管理员权限的功能矩阵：群 / 频道分开设置、写入后读路径（含已预热的资料缓存）立即
// 看到新值、未变报 not modified、零值清空、非 bot 被拒。
func TestSetDefaultAdminRights(t *testing.T) {
	ctx := context.Background()
	group := domain.ChannelAdminRights{DeleteMessages: true, BanUsers: true, InviteUsers: true}
	broadcast := domain.ChannelAdminRights{PostMessages: true, EditMessages: true}

	newBot := func(t *testing.T) (*Service, domain.User) {
		t.Helper()
		svc, users, _, _ := newTestService(t)
		owner := newOwner(t, users, "+1091")
		bot, _, err := svc.CreateBot(ctx, owner.ID, "Rights Bot", "rights_test_bot")
		if err != nil {
			t.Fatalf("create bot: %v", err)
		}
		// 预热资料缓存，确认写入会让它失效。
		if _, found, err := svc.BotInfo(ctx, bot.ID); err != nil || !found {
			t.Fatalf("warm BotInfo: found=%v err=%v", found, err)
		}
		return svc, bot
	}
	profile := func(t *testing.T, svc *Service, botID int64) domain.BotProfile {
		t.Helper()
		p, found, err := svc.BotInfo(ctx, botID)
		if err != nil || !found {
			t.Fatalf("BotInfo: found=%v err=%v", found, err)
		}
		return p
	}

	t.Run("group and broadcast set independently and visible at once", func(t *testing.T) {
		svc, bot := newBot(t)
		if _, err := svc.SetDefaultAdminRights(ctx, bot.ID, false, group); err != nil {
			t.Fatalf("set group: %v", err)
		}
		if _, err := svc.SetDefaultAdminRights(ctx, bot.ID, true, broadcast); err != nil {
			t.Fatalf("set broadcast: %v", err)
		}
		p := profile(t, svc, bot.ID)
		if p.GroupAdminRights != group || p.BroadcastAdminRights != broadcast {
			t.Fatalf("profile rights = group %+v broadcast %+v", p.GroupAdminRights, p.BroadcastAdminRights)
		}
		batch, err := svc.BotInfos(ctx, []int64{bot.ID})
		if err != nil || batch[bot.ID].GroupAdminRights != group || batch[bot.ID].BroadcastAdminRights != broadcast {
			t.Fatalf("batch profile = %+v err=%v", batch[bot.ID], err)
		}
	})

	t.Run("same value is not modified", func(t *testing.T) {
		svc, bot := newBot(t)
		if _, err := svc.SetDefaultAdminRights(ctx, bot.ID, false, group); err != nil {
			t.Fatalf("set group: %v", err)
		}
		if _, err := svc.SetDefaultAdminRights(ctx, bot.ID, false, group); !errors.Is(err, domain.ErrBotAdminRightsNotModified) {
			t.Fatalf("repeat set err = %v, want ErrBotAdminRightsNotModified", err)
		}
		// 频道那组仍是空的，写同一份群权限进去是修改。
		if _, err := svc.SetDefaultAdminRights(ctx, bot.ID, true, group); err != nil {
			t.Fatalf("set broadcast to group rights: %v", err)
		}
		// 从未设置时写零值同样未变。
		svc2, bot2 := newBot(t)
		if _, err := svc2.SetDefaultAdminRights(ctx, bot2.ID, true, domain.ChannelAdminRights{}); !errors.Is(err, domain.ErrBotAdminRightsNotModified) {
			t.Fatalf("clearing unset rights err = %v, want ErrBotAdminRightsNotModified", err)
		}
	})

	t.Run("zero value clears", func(t *testing.T) {
		svc, bot := newBot(t)
		if _, err := svc.SetDefaultAdminRights(ctx, bot.ID, false, group); err != nil {
			t.Fatalf("set group: %v", err)
		}
		if _, err := svc.SetDefaultAdminRights(ctx, bot.ID, false, domain.ChannelAdminRights{}); err != nil {
			t.Fatalf("clear group: %v", err)
		}
		if p := profile(t, svc, bot.ID); p.GroupAdminRights != (domain.ChannelAdminRights{}) {
			t.Fatalf("group rights after clear = %+v, want zero", p.GroupAdminRights)
		}
	})

	t.Run("non-bot rejected", func(t *testing.T) {
		svc, users, _, _ := newTestService(t)
		human := newOwner(t, users, "+1092")
		if _, err := svc.SetDefaultAdminRights(ctx, human.ID, false, group); !errors.Is(err, domain.ErrBotNotFound) {
			t.Fatalf("set on human err = %v, want ErrBotNotFound", err)
		}
	})
}
