package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// bots.setBotMenuButton 带 user_id 时只把 updateBotMenuButton 推给该用户；以 inputUserSelf
// 或机器人自己的 id 为目标表示默认按钮；不存在的用户报 USER_ID_INVALID。专属按钮与全局
// 默认互不影响、default 即删除专属按钮由 storetest.BotUserMenuButtons 覆盖。
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

	// 以机器人自己为目标（inputUserSelf 或自己的 id）也表示默认按钮，客户端常这样传。
	for i, self := range []tg.InputUserClass{&tg.InputUserSelf{}, &tg.InputUser{UserID: f.bot.ID}} {
		url := []string{"https://example.com/self", "https://example.com/own-id"}[i]
		if ok, err := f.router.onBotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
			UserID: self,
			Button: &tg.BotMenuButton{Text: "Open", URL: url},
		}); err != nil || !ok {
			t.Fatalf("self-targeted set %T = %v, %v; want the default set", self, ok, err)
		}
		button, err := f.bots.GetBotMenuButton(f.ctx, f.bot.ID)
		if err != nil || button.Type != domain.BotMenuButtonWebView || button.URL != url {
			t.Fatalf("default after self-targeted set %T = %+v, %v; want web view %s", self, button, err, url)
		}
		if got, err := f.router.onBotsGetBotMenuButton(ctx, self); err != nil {
			t.Fatalf("self-targeted get %T: %v", self, err)
		} else if b, ok := got.(*tg.BotMenuButton); !ok || b.URL != url {
			t.Fatalf("self-targeted get %T = %#v, want the default %s", self, got, url)
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
