package botapi

import (
	"context"
	"net/http"
	"testing"

	"telesrv/internal/domain"
)

type recordingMenuBots struct {
	*fakeBotAPIBots
	setCalls int
}

func (f *recordingMenuBots) SetBotMenuButton(context.Context, int64, domain.BotMenuButton) (int, error) {
	f.setCalls++
	return 1, nil
}

// 菜单按钮只有一份全局默认值。带 chat_id 的请求以前被静默当成全局设置，一个用户的
// 菜单会变成所有人的；在支持按用户存储之前，明确拒绝。
func TestSetChatMenuButtonRejectsPerChatButtonInsteadOfOverwritingDefault(t *testing.T) {
	bots := &recordingMenuBots{fakeBotAPIBots: &fakeBotAPIBots{profile: testBotProfile}}
	h := (&handler{bots: bots, gateway: &fakeBotAPIGateway{}}).routes()

	rec := callMethod(t, h, "setChatMenuButton", `{"chat_id":42,"menu_button":{"type":"commands"}}`)
	if rec.Code != http.StatusBadRequest || decodeAPIResponse(t, rec).Description != "MENU_BUTTON_PER_CHAT_UNSUPPORTED" {
		t.Fatalf("status = %d body = %s, want 400 MENU_BUTTON_PER_CHAT_UNSUPPORTED", rec.Code, rec.Body.String())
	}
	if bots.setCalls != 0 {
		t.Fatalf("SetBotMenuButton called %d times, want the default left untouched", bots.setCalls)
	}

	rec = callMethod(t, h, "setChatMenuButton", `{"menu_button":{"type":"commands"}}`)
	if rec.Code != http.StatusOK || bots.setCalls != 1 {
		t.Fatalf("status = %d calls = %d, want the default button set", rec.Code, bots.setCalls)
	}
}
