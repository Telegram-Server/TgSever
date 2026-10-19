package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// bots.setBotMenuButton 带 user_id 时设置的是该用户专属的按钮，全局默认不受影响；
// 以 inputUserEmpty、inputUserSelf 或机器人自己的 id 为目标仍表示默认按钮。
func TestBotsSetBotMenuButtonPerUserButton(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	ctx := WithUserID(f.ctx, f.bot.ID)
	member := &tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}

	if ok, err := f.router.onBotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
		UserID: member,
		Button: &tg.BotMenuButtonCommands{},
	}); err != nil || !ok {
		t.Fatalf("per-user set = %v, %v; want ok", ok, err)
	}
	// 只把 updateBotMenuButton 推给该用户。
	f.sessions.mu.Lock()
	pushed, msg := append([]int64(nil), f.sessions.pushUserIDs...), f.sessions.userMessage
	f.sessions.mu.Unlock()
	if len(pushed) != 1 || pushed[0] != f.member.ID {
		t.Fatalf("pushed to %v, want only member %d", pushed, f.member.ID)
	}
	if updates, ok := msg.(*tg.Updates); !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed %#v, want one updateBotMenuButton", msg)
	} else if u, ok := updates.Updates[0].(*tg.UpdateBotMenuButton); !ok || u.BotID != f.bot.ID {
		t.Fatalf("pushed update %#v, want updateBotMenuButton for the bot", updates.Updates[0])
	} else if _, ok := u.Button.(*tg.BotMenuButtonCommands); !ok {
		t.Fatalf("pushed button %T, want commands", u.Button)
	}
	button, err := f.bots.GetBotMenuButton(f.ctx, f.bot.ID)
	if err != nil || button.Type != domain.BotMenuButtonDefault {
		t.Fatalf("default button = %+v, %v; want it untouched", button, err)
	}
	if got, err := f.router.onBotsGetBotMenuButton(ctx, member); err != nil {
		t.Fatalf("get member button: %v", err)
	} else if _, ok := got.(*tg.BotMenuButtonCommands); !ok {
		t.Fatalf("member button = %T, want commands", got)
	}
	owner := &tg.InputUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash}
	if got, err := f.router.onBotsGetBotMenuButton(ctx, owner); err != nil {
		t.Fatalf("get owner button: %v", err)
	} else if _, ok := got.(*tg.BotMenuButtonDefault); !ok {
		t.Fatalf("owner button = %T, want the default", got)
	}

	if ok, err := f.router.onBotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
		UserID: &tg.InputUserEmpty{},
		Button: &tg.BotMenuButton{Text: "Open", URL: "https://example.com/global"},
	}); err != nil || !ok {
		t.Fatalf("default set = %v, %v; want ok", ok, err)
	}
	button, err = f.bots.GetBotMenuButton(f.ctx, f.bot.ID)
	if err != nil || button.Type != domain.BotMenuButtonWebView {
		t.Fatalf("default button = %+v, %v; want web view", button, err)
	}
	// 成员的专属按钮不被全局改写覆盖。
	if got, err := f.router.onBotsGetBotMenuButton(ctx, member); err != nil {
		t.Fatalf("get member button: %v", err)
	} else if _, ok := got.(*tg.BotMenuButtonCommands); !ok {
		t.Fatalf("member button after default change = %T, want commands", got)
	}
	// 给成员设 default = 删除专属按钮，回落到全局。
	if ok, err := f.router.onBotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
		UserID: member,
		Button: &tg.BotMenuButtonDefault{},
	}); err != nil || !ok {
		t.Fatalf("per-user reset = %v, %v; want ok", ok, err)
	}
	if got, err := f.router.onBotsGetBotMenuButton(ctx, member); err != nil {
		t.Fatalf("get member button: %v", err)
	} else if b, ok := got.(*tg.BotMenuButton); !ok || b.URL != "https://example.com/global" {
		t.Fatalf("member button after reset = %#v, want the global web view", got)
	}

	// 以机器人自己为目标（inputUserSelf 或自己的 id）也表示默认按钮，客户端常这样传。
	for _, self := range []tg.InputUserClass{&tg.InputUserSelf{}, &tg.InputUser{UserID: f.bot.ID}} {
		if ok, err := f.router.onBotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
			UserID: self,
			Button: &tg.BotMenuButtonDefault{},
		}); err != nil || !ok {
			t.Fatalf("self-targeted set %T = %v, %v; want the default set", self, ok, err)
		}
		if got, err := f.router.onBotsGetBotMenuButton(ctx, self); err != nil {
			t.Fatalf("self-targeted get %T: %v", self, err)
		} else if _, ok := got.(*tg.BotMenuButtonDefault); !ok {
			t.Fatalf("self-targeted get %T = %T, want the default", self, got)
		}
	}

	// 不存在的用户。
	if _, err := f.router.onBotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
		UserID: &tg.InputUser{UserID: 987654321, AccessHash: 1},
		Button: &tg.BotMenuButtonCommands{},
	}); !tgerr.Is(err, "USER_ID_INVALID") {
		t.Fatalf("unknown user err = %v, want USER_ID_INVALID", err)
	}
}
