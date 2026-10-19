package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BotMenuButtonFixture 是一个新建的机器人与两个普通用户。
type BotMenuButtonFixture struct {
	Bots         store.BotStore
	Users        store.UserStore
	BotID        int64
	UserA, UserB int64
	// DeleteBot 非 nil 时额外校验删除机器人会级联清掉它的专属按钮（内存实现不支持删机器人，留 nil）。
	DeleteBot func(context.Context, int64) error
}

func (f BotMenuButtonFixture) get(t *testing.T, user int64) (domain.BotMenuButton, bool) {
	t.Helper()
	button, found, err := f.Bots.GetBotUserMenuButton(context.Background(), f.BotID, user)
	if err != nil {
		t.Fatalf("get user %d menu button: %v", user, err)
	}
	return button, found
}

func (f BotMenuButtonFixture) set(t *testing.T, user int64, button domain.BotMenuButton) {
	t.Helper()
	if err := f.Bots.SetBotUserMenuButton(context.Background(), f.BotID, user, button); err != nil {
		t.Fatalf("set user %d menu button %+v: %v", user, button, err)
	}
}

// BotUserMenuButtons 校验按用户的机器人菜单按钮：各用户互不可见、写 default 即删除
// 专属按钮（回落到全局默认）、全局默认不受影响。
//
// 专属按钮不 bump bot_info_version：那是所有用户共享的版本号，bump 会让每个客户端重拉
// 机器人资料、并让所有实例失效所有人的机器人资料缓存；fixture 的 Users 读出该机器人的
// users 行用来断言这一点。每个子测试经 newFixture 新建自己的机器人。
func BotUserMenuButtons(t *testing.T, newFixture func(t *testing.T) BotMenuButtonFixture) {
	t.Helper()
	ctx := context.Background()
	webApp := domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Open A", URL: "https://a.example/app"}
	commands := domain.BotMenuButton{Type: domain.BotMenuButtonCommands}
	reset := domain.BotMenuButton{Type: domain.BotMenuButtonDefault}

	t.Run("per-user buttons are isolated", func(t *testing.T) {
		f := newFixture(t)
		if _, found := f.get(t, f.UserA); found {
			t.Fatal("fresh bot already has a per-user menu button")
		}
		f.set(t, f.UserA, webApp)
		f.set(t, f.UserB, commands)
		if got, found := f.get(t, f.UserA); !found || got != webApp {
			t.Fatalf("user A button = %+v found=%v, want %+v", got, found, webApp)
		}
		if got, found := f.get(t, f.UserB); !found || got != commands {
			t.Fatalf("user B button = %+v found=%v, want commands", got, found)
		}
		profile, found, err := f.Bots.GetBot(ctx, f.BotID)
		if err != nil || !found {
			t.Fatalf("get bot: found=%v err=%v", found, err)
		}
		if profile.MenuButton.Type != domain.BotMenuButtonDefault {
			t.Fatalf("global default = %+v, want it untouched by per-user writes", profile.MenuButton)
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		f := newFixture(t)
		f.set(t, f.UserA, webApp)
		replaced := domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Open A2", URL: "https://a.example/v2"}
		f.set(t, f.UserA, replaced)
		if got, _ := f.get(t, f.UserA); got != replaced {
			t.Fatalf("user A button = %+v, want %+v", got, replaced)
		}
	})

	// default = 删除专属按钮，回落到全局默认；删除不存在的专属按钮不是错误。
	t.Run("default deletes the per-user button", func(t *testing.T) {
		f := newFixture(t)
		f.set(t, f.UserA, webApp)
		f.set(t, f.UserB, commands)
		f.set(t, f.UserA, reset)
		if got, found := f.get(t, f.UserA); found {
			t.Fatalf("user A button after reset = %+v, want none", got)
		}
		if _, found := f.get(t, f.UserB); !found {
			t.Fatal("resetting user A dropped user B's button")
		}
		f.set(t, f.UserA, reset)
	})

	t.Run("shared bot_info_version untouched", func(t *testing.T) {
		f := newFixture(t)
		version := func() int {
			t.Helper()
			u, found, err := f.Users.ByID(ctx, f.BotID)
			if err != nil || !found {
				t.Fatalf("bot user row: found=%v err=%v", found, err)
			}
			return u.BotInfoVersion
		}
		before := version()
		f.set(t, f.UserA, webApp)
		f.set(t, f.UserA, commands)
		f.set(t, f.UserA, reset)
		if after := version(); after != before {
			t.Fatalf("bot_info_version %d -> %d, want per-user writes to leave the shared version alone", before, after)
		}
	})

	t.Run("unknown bot rejected", func(t *testing.T) {
		f := newFixture(t)
		if err := f.Bots.SetBotUserMenuButton(ctx, f.BotID+1_000_000_000, f.UserA, webApp); err == nil {
			t.Fatal("set on unknown bot succeeded, want an error")
		}
	})

	t.Run("deleting the bot cascades", func(t *testing.T) {
		f := newFixture(t)
		if f.DeleteBot == nil {
			t.Skip("store cannot delete bots")
		}
		f.set(t, f.UserB, commands)
		if err := f.DeleteBot(ctx, f.BotID); err != nil {
			t.Fatalf("delete bot: %v", err)
		}
		if _, found := f.get(t, f.UserB); found {
			t.Fatal("deleting the bot left its per-user menu buttons behind")
		}
	})
}
