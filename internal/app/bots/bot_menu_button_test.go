package bots

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

var globalMenuButton = domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Global", URL: "https://example.com/global"}

// menuButtonFixture 是一个已设全局 web_app 按钮的机器人和两个普通用户。
type menuButtonFixture struct {
	svc              *Service
	users            *memory.UserStore
	bot              domain.User
	viewerA, viewerB domain.User
}

func newMenuButtonFixture(t *testing.T) menuButtonFixture {
	t.Helper()
	svc, users, _, _ := newTestService(t)
	owner := newOwner(t, users, "+2101")
	f := menuButtonFixture{
		svc:     svc,
		users:   users,
		viewerA: newOwner(t, users, "+2102"),
		viewerB: newOwner(t, users, "+2103"),
	}
	f.bot = makeBot(t, svc, owner, "Scoped Menu Bot", "scoped_menu_bot")
	if _, err := svc.SetBotMenuButton(context.Background(), f.bot.ID, globalMenuButton); err != nil {
		t.Fatalf("set global: %v", err)
	}
	return f
}

func (f menuButtonFixture) setPersonal(t *testing.T, userID int64, button domain.BotMenuButton) {
	t.Helper()
	if err := f.svc.SetBotMenuButtonForUser(context.Background(), f.bot.ID, userID, button); err != nil {
		t.Fatalf("set personal for %d: %v", userID, err)
	}
}

// 专属按钮（去掉首尾空白后）只对该用户生效，别人仍看到全局默认。
func TestBotMenuButtonForUserAppliesOnlyToThatViewer(t *testing.T) {
	f := newMenuButtonFixture(t)
	ctx := context.Background()
	f.setPersonal(t, f.viewerA.ID, domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: " Mine ", URL: " https://example.com/a "})

	if got, err := f.svc.BotMenuButtonForUser(ctx, f.bot.ID, f.viewerA.ID); err != nil || got.Text != "Mine" || got.URL != "https://example.com/a" {
		t.Fatalf("viewer A button = %+v, %v; want the trimmed personal button", got, err)
	}
	if got, err := f.svc.BotMenuButtonForUser(ctx, f.bot.ID, f.viewerB.ID); err != nil || got != globalMenuButton {
		t.Fatalf("viewer B button = %+v, %v; want the global default", got, err)
	}
}

// 专属按钮不改所有人共享的状态：bot_info_version、全局按钮、main mini app 都不动。
func TestBotMenuButtonForUserLeavesSharedStateAlone(t *testing.T) {
	f := newMenuButtonFixture(t)
	ctx := context.Background()
	before, _, _ := f.users.ByID(ctx, f.bot.ID)
	f.setPersonal(t, f.viewerA.ID, domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Mine", URL: "https://example.com/a"})

	if after, _, _ := f.users.ByID(ctx, f.bot.ID); after.BotInfoVersion != before.BotInfoVersion {
		t.Fatalf("bot_info_version %d -> %d, want untouched", before.BotInfoVersion, after.BotInfoVersion)
	}
	if got, err := f.svc.GetBotMenuButton(ctx, f.bot.ID); err != nil || got != globalMenuButton {
		t.Fatalf("global button = %+v, %v; want untouched", got, err)
	}
	if app, found, err := f.svc.GetMainBotApp(ctx, f.bot.ID); err != nil || !found || app.URL != globalMenuButton.URL {
		t.Fatalf("main app = %+v found=%v err=%v; want the global button's app", app, found, err)
	}
}

// 设 default 即删除专属按钮，回落到全局默认。
func TestBotMenuButtonForUserDefaultFallsBackToGlobal(t *testing.T) {
	f := newMenuButtonFixture(t)
	f.setPersonal(t, f.viewerA.ID, domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Mine", URL: "https://example.com/a"})
	f.setPersonal(t, f.viewerA.ID, domain.BotMenuButton{Type: domain.BotMenuButtonDefault})

	if got, err := f.svc.BotMenuButtonForUser(context.Background(), f.bot.ID, f.viewerA.ID); err != nil || got != globalMenuButton {
		t.Fatalf("viewer A after reset = %+v, %v; want the global default", got, err)
	}
}

// 按钮校验与全局一致；目标必须是存在的、非机器人自身的用户。
func TestSetBotMenuButtonForUserValidates(t *testing.T) {
	f := newMenuButtonFixture(t)
	commands := domain.BotMenuButton{Type: domain.BotMenuButtonCommands}
	for _, tc := range []struct {
		name   string
		userID int64
		button domain.BotMenuButton
		want   error
	}{
		{"http url", f.viewerA.ID, domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "x", URL: "http://insecure"}, domain.ErrBotMenuButtonInvalid},
		{"unknown user", 987654321, commands, domain.ErrUserNotFound},
		{"the bot itself", f.bot.ID, commands, domain.ErrUserNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := f.svc.SetBotMenuButtonForUser(context.Background(), f.bot.ID, tc.userID, tc.button); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
