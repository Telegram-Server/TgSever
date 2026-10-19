package rpc

import (
	"testing"

	"telesrv/internal/domain"
)

// Bot API 的全局 setChatMenuButton 以用户 0 调网关，应设置全局默认按钮。
func TestBotAPISetChatMenuButtonWithoutUserSetsDefault(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	if err := f.router.BotAPISetChatMenuButton(f.ctx, f.bot.ID, 0, domain.BotMenuButton{Type: domain.BotMenuButtonCommands}); err != nil {
		t.Fatalf("BotAPISetChatMenuButton(default): %v", err)
	}
	button, err := f.bots.GetBotMenuButton(f.ctx, f.bot.ID)
	if err != nil || button.Type != domain.BotMenuButtonCommands {
		t.Fatalf("default button = %+v, %v; want commands", button, err)
	}
}
