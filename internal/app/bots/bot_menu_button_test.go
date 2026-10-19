package bots

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestBotMenuButtonForUser(t *testing.T) {
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2101")
	viewerA := newOwner(t, users, "+2102")
	viewerB := newOwner(t, users, "+2103")
	bot := makeBot(t, svc, owner, "Scoped Menu Bot", "scoped_menu_bot")
	ctx := context.Background()

	global := domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Global", URL: "https://example.com/global"}
	if _, err := svc.SetBotMenuButton(ctx, bot.ID, global); err != nil {
		t.Fatalf("set global: %v", err)
	}
	before, _, _ := users.ByID(ctx, bot.ID)
	personal := domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: " Mine ", URL: " https://example.com/a "}
	if err := svc.SetBotMenuButtonForUser(ctx, bot.ID, viewerA.ID, personal); err != nil {
		t.Fatalf("set personal: %v", err)
	}
	// 专属按钮不 bump 所有人共享的 bot_info_version。
	if after, _, _ := users.ByID(ctx, bot.ID); after.BotInfoVersion != before.BotInfoVersion {
		t.Fatalf("bot_info_version %d -> %d, want untouched", before.BotInfoVersion, after.BotInfoVersion)
	}
	if got, err := svc.BotMenuButtonForUser(ctx, bot.ID, viewerA.ID); err != nil || got.Text != "Mine" || got.URL != "https://example.com/a" {
		t.Fatalf("viewer A button = %+v, %v; want the trimmed personal button", got, err)
	}
	if got, err := svc.BotMenuButtonForUser(ctx, bot.ID, viewerB.ID); err != nil || got != global {
		t.Fatalf("viewer B button = %+v, %v; want the global default", got, err)
	}
	if got, err := svc.GetBotMenuButton(ctx, bot.ID); err != nil || got != global {
		t.Fatalf("global button = %+v, %v; want untouched", got, err)
	}
	// 专属按钮不会生成/改写 main mini app（那是所有人共享的）。
	if app, found, err := svc.GetMainBotApp(ctx, bot.ID); err != nil || !found || app.URL != global.URL {
		t.Fatalf("main app = %+v found=%v err=%v; want the global button's app", app, found, err)
	}

	// default 删除专属按钮，回落到全局。
	if err := svc.SetBotMenuButtonForUser(ctx, bot.ID, viewerA.ID, domain.BotMenuButton{Type: domain.BotMenuButtonDefault}); err != nil {
		t.Fatalf("reset personal: %v", err)
	}
	if got, err := svc.BotMenuButtonForUser(ctx, bot.ID, viewerA.ID); err != nil || got != global {
		t.Fatalf("viewer A after reset = %+v, %v; want the global default", got, err)
	}

	// 校验规则与全局一致。
	if err := svc.SetBotMenuButtonForUser(ctx, bot.ID, viewerA.ID, domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "x", URL: "http://insecure"}); !errors.Is(err, domain.ErrBotMenuButtonInvalid) {
		t.Fatalf("http url err = %v, want ErrBotMenuButtonInvalid", err)
	}
	// 目标必须是存在的、非机器人自身的用户。
	if err := svc.SetBotMenuButtonForUser(ctx, bot.ID, 987654321, domain.BotMenuButton{Type: domain.BotMenuButtonCommands}); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("unknown user err = %v, want ErrUserNotFound", err)
	}
	if err := svc.SetBotMenuButtonForUser(ctx, bot.ID, bot.ID, domain.BotMenuButton{Type: domain.BotMenuButtonCommands}); !errors.Is(err, domain.ErrUserNotFound) {
		t.Fatalf("self target err = %v, want ErrUserNotFound", err)
	}
}
