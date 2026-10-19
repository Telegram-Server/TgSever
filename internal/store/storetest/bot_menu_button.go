package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BotUserMenuButtons 校验按用户的机器人菜单按钮：各用户互不可见、写 default 即删除
// 专属按钮（回落到全局默认）、全局默认不受影响。
//
// 专属按钮不 bump bot_info_version：那是所有用户共享的版本号，bump 会让每个客户端重拉
// 机器人资料、并让所有实例失效所有人的机器人资料缓存；users 读出该机器人的 users 行用来断言这一点。
// deleteBot 非 nil 时再校验删除机器人会级联清掉它的专属按钮（内存实现不支持删机器人，传 nil）。
func BotUserMenuButtons(t *testing.T, s store.BotStore, users store.UserStore, botID, userA, userB int64, deleteBot func(context.Context, int64) error) {
	t.Helper()
	ctx := context.Background()
	get := func(user int64) (domain.BotMenuButton, bool) {
		t.Helper()
		button, found, err := s.GetBotUserMenuButton(ctx, botID, user)
		if err != nil {
			t.Fatalf("get user %d menu button: %v", user, err)
		}
		return button, found
	}
	version := func() int {
		t.Helper()
		u, found, err := users.ByID(ctx, botID)
		if err != nil || !found {
			t.Fatalf("bot user row: found=%v err=%v", found, err)
		}
		return u.BotInfoVersion
	}
	if _, found := get(userA); found {
		t.Fatal("fresh bot already has a per-user menu button")
	}
	before := version()

	webApp := domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Open A", URL: "https://a.example/app"}
	if err := s.SetBotUserMenuButton(ctx, botID, userA, webApp); err != nil {
		t.Fatalf("set user A button: %v", err)
	}
	if err := s.SetBotUserMenuButton(ctx, botID, userB, domain.BotMenuButton{Type: domain.BotMenuButtonCommands}); err != nil {
		t.Fatalf("set user B button: %v", err)
	}
	if got, found := get(userA); !found || got != webApp {
		t.Fatalf("user A button = %+v found=%v, want %+v", got, found, webApp)
	}
	if got, found := get(userB); !found || got.Type != domain.BotMenuButtonCommands || got.Text != "" || got.URL != "" {
		t.Fatalf("user B button = %+v found=%v, want commands", got, found)
	}
	profile, found, err := s.GetBot(ctx, botID)
	if err != nil || !found {
		t.Fatalf("get bot: found=%v err=%v", found, err)
	}
	if profile.MenuButton.Type != domain.BotMenuButtonDefault {
		t.Fatalf("global default = %+v, want it untouched by per-user writes", profile.MenuButton)
	}

	// 覆盖写。
	replaced := domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Open A2", URL: "https://a.example/v2"}
	if err := s.SetBotUserMenuButton(ctx, botID, userA, replaced); err != nil {
		t.Fatalf("replace user A button: %v", err)
	}
	if got, _ := get(userA); got != replaced {
		t.Fatalf("user A button = %+v, want %+v", got, replaced)
	}

	// default = 删除专属按钮，回落到全局默认。
	if err := s.SetBotUserMenuButton(ctx, botID, userA, domain.BotMenuButton{Type: domain.BotMenuButtonDefault}); err != nil {
		t.Fatalf("reset user A button: %v", err)
	}
	if got, found := get(userA); found {
		t.Fatalf("user A button after reset = %+v, want none", got)
	}
	if _, found := get(userB); !found {
		t.Fatal("resetting user A dropped user B's button")
	}
	// 删除不存在的专属按钮不是错误。
	if err := s.SetBotUserMenuButton(ctx, botID, userA, domain.BotMenuButton{Type: domain.BotMenuButtonDefault}); err != nil {
		t.Fatalf("reset absent button: %v", err)
	}
	if after := version(); after != before {
		t.Fatalf("bot_info_version %d -> %d, want per-user writes to leave the shared version alone", before, after)
	}

	if err := s.SetBotUserMenuButton(ctx, botID+1_000_000_000, userA, webApp); err == nil {
		t.Fatal("set on unknown bot succeeded, want an error")
	}

	if deleteBot == nil {
		return
	}
	if err := deleteBot(ctx, botID); err != nil {
		t.Fatalf("delete bot: %v", err)
	}
	if _, found := get(userB); found {
		t.Fatal("deleting the bot left its per-user menu buttons behind")
	}
}
