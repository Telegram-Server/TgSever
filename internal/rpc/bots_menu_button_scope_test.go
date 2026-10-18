package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// bots.setBotMenuButton 带 user_id 时设置的是该用户专属的按钮。本服只存一份全局
// 默认值，以前会把专属按钮写成全局的；在支持按用户存储之前，明确拒绝。
func TestBotsSetBotMenuButtonRejectsPerUserButton(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	ctx := WithUserID(f.ctx, f.bot.ID)

	_, err := f.router.onBotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
		UserID: &tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash},
		Button: &tg.BotMenuButtonCommands{},
	})
	if !tgerr.Is(err, "MENU_BUTTON_PER_CHAT_UNSUPPORTED") {
		t.Fatalf("err = %v, want MENU_BUTTON_PER_CHAT_UNSUPPORTED", err)
	}
	button, err := f.bots.GetBotMenuButton(f.ctx, f.bot.ID)
	if err != nil || button.Type != domain.BotMenuButtonDefault {
		t.Fatalf("default button = %+v, %v; want it untouched", button, err)
	}

	if ok, err := f.router.onBotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
		UserID: &tg.InputUserEmpty{},
		Button: &tg.BotMenuButtonCommands{},
	}); err != nil || !ok {
		t.Fatalf("default set = %v, %v; want ok", ok, err)
	}
	button, err = f.bots.GetBotMenuButton(f.ctx, f.bot.ID)
	if err != nil || button.Type != domain.BotMenuButtonCommands {
		t.Fatalf("default button = %+v, %v; want commands", button, err)
	}

	// 以机器人自己为目标（inputUserSelf 或自己的 id）也表示默认按钮，客户端常这样传。
	for _, self := range []tg.InputUserClass{&tg.InputUserSelf{}, &tg.InputUser{UserID: f.bot.ID}} {
		if ok, err := f.router.onBotsSetBotMenuButton(ctx, &tg.BotsSetBotMenuButtonRequest{
			UserID: self,
			Button: &tg.BotMenuButtonDefault{},
		}); err != nil || !ok {
			t.Fatalf("self-targeted set %T = %v, %v; want the default set", self, ok, err)
		}
	}
}
