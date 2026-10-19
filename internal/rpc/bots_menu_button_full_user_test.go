package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appbots "telesrv/internal/app/bots"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type userMenuButtonFixture struct {
	ctx     context.Context
	a, b, c domain.User
	bot     domain.User
	router  *Router
	bots    *appbots.Service
}

func newUserMenuButtonFixture(t *testing.T) userMenuButtonFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	mk := func(hash int64, phone, name string) domain.User {
		u, err := userStore.Create(ctx, domain.User{AccessHash: hash, Phone: phone, FirstName: name})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return u
	}
	a := mk(3101, "15550009301", "A")
	b := mk(3102, "15550009302", "B")
	c := mk(3103, "15550009303", "C")
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	botStore := memory.NewBotStore(userStore)
	bot, _, err := botStore.CreateBotAccount(ctx, domain.User{AccessHash: 3201, FirstName: "MenuBot", Username: "PerUserMenuBot"}, domain.BotProfile{
		OwnerUserID: a.ID,
		TokenSecret: "secret",
	})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	botsService := appbots.NewService(userStore, botStore, messageStore)
	router := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Messages: appmessages.NewService(messageStore, dialogStore),
		Bots:     botsService,
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	return userMenuButtonFixture{ctx: ctx, a: a, b: b, c: c, bot: bot, router: router, bots: botsService}
}

func (f userMenuButtonFixture) setFor(t *testing.T, target tg.InputUserClass, button tg.BotMenuButtonClass) {
	t.Helper()
	if ok, err := f.router.onBotsSetBotMenuButton(WithUserID(f.ctx, f.bot.ID), &tg.BotsSetBotMenuButtonRequest{UserID: target, Button: button}); err != nil || !ok {
		t.Fatalf("set menu button for %#v = %v, %v", target, ok, err)
	}
}

func (f userMenuButtonFixture) fullMenuButton(t *testing.T, viewer domain.User) tg.BotMenuButtonClass {
	t.Helper()
	full, err := f.router.onUsersGetFullUser(WithUserID(f.ctx, viewer.ID), inputUser(f.bot))
	if err != nil {
		t.Fatalf("getFullUser by %d: %v", viewer.ID, err)
	}
	info, ok := full.FullUser.GetBotInfo()
	if !ok {
		t.Fatalf("getFullUser by %d: no bot_info", viewer.ID)
	}
	button, ok := info.GetMenuButton()
	if !ok {
		t.Fatalf("getFullUser by %d: no menu_button", viewer.ID)
	}
	return button
}

func wantWebViewButton(t *testing.T, who string, got tg.BotMenuButtonClass, url string) {
	t.Helper()
	b, ok := got.(*tg.BotMenuButton)
	if !ok || b.URL != url {
		t.Fatalf("%s sees %#v, want web view %s", who, got, url)
	}
}

// 两个用户各自的专属按钮只有自己看得到；第三个用户看到全局默认。userFull 投影缓存
// 按 (viewer, target) 存，专属按钮绝不能经缓存串给其他人。
func TestGetFullUserBotInfoMenuButtonIsPerViewer(t *testing.T) {
	f := newUserMenuButtonFixture(t)
	f.setFor(t, &tg.InputUserEmpty{}, &tg.BotMenuButton{Text: "Global", URL: "https://example.com/global"})

	// 先让三个人都把机器人资料读进缓存。
	for _, viewer := range []domain.User{f.a, f.b, f.c} {
		wantWebViewButton(t, "warm-up", f.fullMenuButton(t, viewer), "https://example.com/global")
	}

	f.setFor(t, inputUser(f.a), &tg.BotMenuButton{Text: "A", URL: "https://example.com/a"})
	// 只失效 A 看机器人的那一份，B、C 的缓存保留。
	if _, ok := f.router.userFullProjectionCache.Lookup(f.a.ID, f.bot.ID); ok {
		t.Fatal("A's cached bot profile survived A's per-user button change")
	}
	for _, viewer := range []domain.User{f.b, f.c} {
		if _, ok := f.router.userFullProjectionCache.Lookup(viewer.ID, f.bot.ID); !ok {
			t.Fatalf("user %d's cached bot profile was dropped by A's per-user change", viewer.ID)
		}
	}
	f.setFor(t, inputUser(f.b), &tg.BotMenuButtonCommands{})

	for i := 0; i < 2; i++ { // 第二轮全部命中缓存。
		wantWebViewButton(t, "A", f.fullMenuButton(t, f.a), "https://example.com/a")
		if got := f.fullMenuButton(t, f.b); func() bool { _, ok := got.(*tg.BotMenuButtonCommands); return !ok }() {
			t.Fatalf("B sees %#v, want commands", got)
		}
		wantWebViewButton(t, "C", f.fullMenuButton(t, f.c), "https://example.com/global")
	}

	// 改全局默认：没有专属按钮的人看到新默认，有专属按钮的人不受影响。
	f.setFor(t, &tg.InputUserSelf{}, &tg.BotMenuButton{Text: "Global2", URL: "https://example.com/global2"})
	wantWebViewButton(t, "A", f.fullMenuButton(t, f.a), "https://example.com/a")
	wantWebViewButton(t, "C", f.fullMenuButton(t, f.c), "https://example.com/global2")
}

// Bot API 的全局 setChatMenuButton 以用户 0 调网关，应设置全局默认按钮。
func TestBotAPISetChatMenuButtonWithoutUserSetsDefault(t *testing.T) {
	f := newUserMenuButtonFixture(t)

	if err := f.router.BotAPISetChatMenuButton(f.ctx, f.bot.ID, 0, domain.BotMenuButton{Type: domain.BotMenuButtonCommands}); err != nil {
		t.Fatalf("BotAPISetChatMenuButton(default): %v", err)
	}
	button, err := f.bots.GetBotMenuButton(f.ctx, f.bot.ID)
	if err != nil || button.Type != domain.BotMenuButtonCommands {
		t.Fatalf("default button = %+v, %v; want commands", button, err)
	}
}

// Bot API setChatMenuButton(chat_id) 经 Router 写专属按钮：同样只失效该用户的缓存，
// getChatMenuButton(chat_id) 读回该用户生效的按钮；chat_id 不是普通用户时报 USER_ID_INVALID。
func TestBotAPIChatMenuButtonGatewayIsPerUser(t *testing.T) {
	f := newUserMenuButtonFixture(t)
	f.setFor(t, &tg.InputUserEmpty{}, &tg.BotMenuButton{Text: "Global", URL: "https://example.com/global"})
	for _, viewer := range []domain.User{f.a, f.b} {
		wantWebViewButton(t, "warm-up", f.fullMenuButton(t, viewer), "https://example.com/global")
	}

	personal := domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "A", URL: "https://example.com/a"}
	if err := f.router.BotAPISetChatMenuButton(f.ctx, f.bot.ID, f.a.ID, personal); err != nil {
		t.Fatalf("set via bot api: %v", err)
	}
	if _, ok := f.router.userFullProjectionCache.Lookup(f.b.ID, f.bot.ID); !ok {
		t.Fatal("B's cached bot profile was dropped by A's per-user change")
	}
	wantWebViewButton(t, "A", f.fullMenuButton(t, f.a), "https://example.com/a")
	wantWebViewButton(t, "B", f.fullMenuButton(t, f.b), "https://example.com/global")
	if got, err := f.router.BotAPIGetChatMenuButton(f.ctx, f.bot.ID, f.a.ID); err != nil || got != personal {
		t.Fatalf("get A via bot api = %+v, %v", got, err)
	}
	if got, err := f.router.BotAPIGetChatMenuButton(f.ctx, f.bot.ID, f.b.ID); err != nil || got.URL != "https://example.com/global" {
		t.Fatalf("get B via bot api = %+v, %v; want the default", got, err)
	}

	if err := f.router.BotAPISetChatMenuButton(f.ctx, f.bot.ID, 987654321, personal); err == nil || !strings.Contains(err.Error(), "USER_ID_INVALID") {
		t.Fatalf("unknown user err = %v, want USER_ID_INVALID", err)
	}
	if _, err := f.router.BotAPIGetChatMenuButton(f.ctx, f.bot.ID, 987654321); err == nil || !strings.Contains(err.Error(), "USER_ID_INVALID") {
		t.Fatalf("unknown user get err = %v, want USER_ID_INVALID", err)
	}
	// 非机器人不能调用。
	if err := f.router.BotAPISetChatMenuButton(f.ctx, f.c.ID, f.a.ID, personal); err == nil {
		t.Fatal("non-bot caller set a menu button")
	}
}

// 点菜单按钮打开 Mini App（requestWebView from_bot_menu，客户端未带 url）用当前用户的专属 URL。
func TestRequestWebViewFromBotMenuUsesViewerButton(t *testing.T) {
	f := newUserMenuButtonFixture(t)
	f.setFor(t, &tg.InputUserEmpty{}, &tg.BotMenuButton{Text: "Global", URL: "https://example.com/global"})
	f.setFor(t, inputUser(f.a), &tg.BotMenuButton{Text: "A", URL: "https://example.com/a"})

	open := func(viewer domain.User) string {
		t.Helper()
		got, err := f.router.onMessagesRequestWebView(WithUserID(f.ctx, viewer.ID), &tg.MessagesRequestWebViewRequest{
			FromBotMenu: true,
			Peer:        inputPeerUser(f.bot),
			Bot:         inputUser(f.bot),
			Platform:    "tdesktop",
		})
		if err != nil {
			t.Fatalf("requestWebView by %d: %v", viewer.ID, err)
		}
		return got.URL
	}
	if got := open(f.a); !strings.HasPrefix(got, "https://example.com/a") {
		t.Fatalf("A opens %q, want the personal URL", got)
	}
	if got := open(f.b); !strings.HasPrefix(got, "https://example.com/global") {
		t.Fatalf("B opens %q, want the global URL", got)
	}

	// 专属按钮是 commands 时，从菜单打开 webview 不能回落到全局 URL。
	f.setFor(t, inputUser(f.b), &tg.BotMenuButtonCommands{})
	if _, err := f.router.onMessagesRequestWebView(WithUserID(f.ctx, f.b.ID), &tg.MessagesRequestWebViewRequest{
		FromBotMenu: true,
		Peer:        inputPeerUser(f.bot),
		Bot:         inputUser(f.bot),
		Platform:    "tdesktop",
	}); err == nil {
		t.Fatal("B opened a menu web view although B's button is commands")
	}
}
