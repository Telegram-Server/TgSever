package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BotDefaultAdminRightsFixture 是一个新建的机器人。
type BotDefaultAdminRightsFixture struct {
	Bots  store.BotStore
	Users store.UserStore
	BotID int64
}

// botBatchReader 是 GetBots 批量读路径（userFull / 频道 full info 富化走这里），
// 不在 store.BotStore 接口里，两个实现都提供。
type botBatchReader interface {
	GetBots(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error)
}

// BotDefaultAdminRights 校验机器人的群默认管理员权限与频道默认管理员权限：
// 两组互不影响、单点读与批量读都能读回、写零值即清空、每次写入 bump bot_info_version
// （跨实例失效 userFull 投影靠它）、未知机器人报 ErrBotNotFound。
func BotDefaultAdminRights(t *testing.T, newFixture func(t *testing.T) BotDefaultAdminRightsFixture) {
	t.Helper()
	ctx := context.Background()
	group := domain.ChannelAdminRights{DeleteMessages: true, BanUsers: true, InviteUsers: true, PinMessages: true}
	broadcast := domain.ChannelAdminRights{PostMessages: true, EditMessages: true, ChangeInfo: true}

	read := func(t *testing.T, f BotDefaultAdminRightsFixture) (single, batch domain.BotProfile) {
		t.Helper()
		single, found, err := f.Bots.GetBot(ctx, f.BotID)
		if err != nil || !found {
			t.Fatalf("get bot: found=%v err=%v", found, err)
		}
		reader, ok := f.Bots.(botBatchReader)
		if !ok {
			t.Fatal("bot store has no GetBots batch reader")
		}
		profiles, err := reader.GetBots(ctx, []int64{f.BotID})
		if err != nil {
			t.Fatalf("get bots: %v", err)
		}
		batch, found = profiles[f.BotID]
		if !found {
			t.Fatal("batch read lost the bot")
		}
		return single, batch
	}
	set := func(t *testing.T, f BotDefaultAdminRightsFixture, forChannels bool, rights domain.ChannelAdminRights) int {
		t.Helper()
		version, err := f.Bots.SetBotDefaultAdminRights(ctx, f.BotID, forChannels, rights)
		if err != nil {
			t.Fatalf("set default admin rights (channels=%v): %v", forChannels, err)
		}
		return version
	}

	t.Run("fresh bot has no default rights", func(t *testing.T) {
		f := newFixture(t)
		single, batch := read(t, f)
		for _, p := range []domain.BotProfile{single, batch} {
			if p.GroupAdminRights != (domain.ChannelAdminRights{}) || p.BroadcastAdminRights != (domain.ChannelAdminRights{}) {
				t.Fatalf("fresh bot rights = group %+v broadcast %+v, want zero", p.GroupAdminRights, p.BroadcastAdminRights)
			}
		}
	})

	t.Run("group and broadcast are stored separately", func(t *testing.T) {
		f := newFixture(t)
		set(t, f, false, group)
		single, batch := read(t, f)
		for _, p := range []domain.BotProfile{single, batch} {
			if p.GroupAdminRights != group || p.BroadcastAdminRights != (domain.ChannelAdminRights{}) {
				t.Fatalf("after group write: group %+v broadcast %+v", p.GroupAdminRights, p.BroadcastAdminRights)
			}
		}
		set(t, f, true, broadcast)
		single, batch = read(t, f)
		for _, p := range []domain.BotProfile{single, batch} {
			if p.GroupAdminRights != group || p.BroadcastAdminRights != broadcast {
				t.Fatalf("after broadcast write: group %+v broadcast %+v", p.GroupAdminRights, p.BroadcastAdminRights)
			}
		}
	})

	t.Run("zero rights clear", func(t *testing.T) {
		f := newFixture(t)
		set(t, f, false, group)
		set(t, f, true, broadcast)
		set(t, f, false, domain.ChannelAdminRights{})
		single, _ := read(t, f)
		if single.GroupAdminRights != (domain.ChannelAdminRights{}) || single.BroadcastAdminRights != broadcast {
			t.Fatalf("after clearing group: group %+v broadcast %+v", single.GroupAdminRights, single.BroadcastAdminRights)
		}
	})

	t.Run("writes bump bot_info_version", func(t *testing.T) {
		f := newFixture(t)
		u, found, err := f.Users.ByID(ctx, f.BotID)
		if err != nil || !found {
			t.Fatalf("bot user row: found=%v err=%v", found, err)
		}
		v1 := set(t, f, false, group)
		v2 := set(t, f, true, broadcast)
		if v1 <= u.BotInfoVersion || v2 <= v1 {
			t.Fatalf("versions %d -> %d -> %d, want strictly increasing", u.BotInfoVersion, v1, v2)
		}
		u, _, err = f.Users.ByID(ctx, f.BotID)
		if err != nil || u.BotInfoVersion != v2 {
			t.Fatalf("users.bot_info_version = %d err=%v, want %d", u.BotInfoVersion, err, v2)
		}
	})

	t.Run("unknown bot rejected", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.Bots.SetBotDefaultAdminRights(ctx, f.BotID+1_000_000_000, false, group)
		if !errors.Is(err, domain.ErrBotNotFound) {
			t.Fatalf("set on unknown bot err = %v, want ErrBotNotFound", err)
		}
	})
}
