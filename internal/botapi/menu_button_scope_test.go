package botapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

type recordingMenuBots struct {
	*fakeBotAPIBots
	setCalls int
	set      domain.BotMenuButton
}

func (f *recordingMenuBots) SetBotMenuButton(_ context.Context, _ int64, button domain.BotMenuButton) (int, error) {
	f.setCalls++
	f.set = button
	return 1, nil
}

func (f *recordingMenuBots) GetBotMenuButton(context.Context, int64) (domain.BotMenuButton, error) {
	return domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Global", URL: "https://example.com/global"}, nil
}

// recordingMenuGateway 记录按用户的菜单按钮读写（生产里由 rpc.Router 实现，负责缓存失效与推送）。
type recordingMenuGateway struct {
	*fakeBotAPIGateway
	perUser map[int64]domain.BotMenuButton
}

func (g *recordingMenuGateway) BotAPISetChatMenuButton(_ context.Context, _ int64, userID int64, button domain.BotMenuButton) error {
	if userID == 404 {
		return errors.New("USER_ID_INVALID")
	}
	if button.Type == domain.BotMenuButtonDefault {
		delete(g.perUser, userID)
		return nil
	}
	g.perUser[userID] = button
	return nil
}

func (g *recordingMenuGateway) BotAPIGetChatMenuButton(_ context.Context, _ int64, userID int64) (domain.BotMenuButton, error) {
	if button, ok := g.perUser[userID]; ok {
		return button, nil
	}
	return domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Global", URL: "https://example.com/global"}, nil
}

// setChatMenuButton / getChatMenuButton 的 chat_id（私聊 = 用户 id）读写该用户的专属按钮；
// 不带 chat_id 仍是全局默认；群/频道的 chat_id 不是私聊，报错且不改全局默认。
func TestChatMenuButtonPerPrivateChat(t *testing.T) {
	bots := &recordingMenuBots{fakeBotAPIBots: &fakeBotAPIBots{profile: testBotProfile}}
	gateway := &recordingMenuGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}, perUser: map[int64]domain.BotMenuButton{}}
	h := (&handler{bots: bots, gateway: gateway}).routes()

	rec := callMethod(t, h, "setChatMenuButton", `{"chat_id":42,"menu_button":{"type":"web_app","text":"Mine","web_app":{"url":"https://example.com/42"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("per-chat set status = %d body = %s", rec.Code, rec.Body.String())
	}
	if bots.setCalls != 0 {
		t.Fatalf("per-chat set touched the default button (%d calls)", bots.setCalls)
	}
	if got := gateway.perUser[42]; got.Type != domain.BotMenuButtonWebView || got.URL != "https://example.com/42" {
		t.Fatalf("user 42 button = %+v", got)
	}

	rec = callMethod(t, h, "getChatMenuButton", `{"chat_id":42}`)
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "https://example.com/42") {
		t.Fatalf("get user 42 = %d %s, want the personal button", rec.Code, body)
	}
	rec = callMethod(t, h, "getChatMenuButton", `{"chat_id":"43"}`)
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "https://example.com/global") {
		t.Fatalf("get user 43 = %d %s, want the default button", rec.Code, body)
	}

	// default = 删除专属按钮。
	rec = callMethod(t, h, "setChatMenuButton", `{"chat_id":42,"menu_button":{"type":"default"}}`)
	if _, ok := gateway.perUser[42]; rec.Code != http.StatusOK || ok {
		t.Fatalf("reset status = %d, still stored = %v", rec.Code, ok)
	}

	// 群（负 chat_id）不是私聊。
	for _, body := range []string{
		`{"chat_id":-1001234,"menu_button":{"type":"commands"}}`,
		`{"chat_id":"@somegroup","menu_button":{"type":"commands"}}`,
		`{"chat_id":"abc","menu_button":{"type":"commands"}}`,
	} {
		rec = callMethod(t, h, "setChatMenuButton", body)
		if rec.Code != http.StatusBadRequest || decodeAPIResponse(t, rec).Description != "CHAT_ID_INVALID" {
			t.Fatalf("%s: status = %d body = %s, want 400 CHAT_ID_INVALID", body, rec.Code, rec.Body.String())
		}
	}
	rec = callMethod(t, h, "getChatMenuButton", `{"chat_id":-1001234}`)
	if rec.Code != http.StatusBadRequest || decodeAPIResponse(t, rec).Description != "CHAT_ID_INVALID" {
		t.Fatalf("group get: status = %d body = %s, want 400 CHAT_ID_INVALID", rec.Code, rec.Body.String())
	}
	// 网关拒绝（未知用户）原样报出。
	rec = callMethod(t, h, "setChatMenuButton", `{"chat_id":404,"menu_button":{"type":"commands"}}`)
	if rec.Code != http.StatusBadRequest || decodeAPIResponse(t, rec).Description != "USER_ID_INVALID" {
		t.Fatalf("unknown user: status = %d body = %s, want 400 USER_ID_INVALID", rec.Code, rec.Body.String())
	}
	if bots.setCalls != 0 {
		t.Fatalf("rejected per-chat sets touched the default button (%d calls)", bots.setCalls)
	}

	// 不带 chat_id（或 0）= 全局默认。
	rec = callMethod(t, h, "setChatMenuButton", `{"menu_button":{"type":"commands"}}`)
	if rec.Code != http.StatusOK || bots.setCalls != 1 || bots.set.Type != domain.BotMenuButtonCommands {
		t.Fatalf("status = %d calls = %d, want the default button set", rec.Code, bots.setCalls)
	}
	rec = callMethod(t, h, "setChatMenuButton", `{"chat_id":0,"menu_button":{"type":"default"}}`)
	if rec.Code != http.StatusOK || bots.setCalls != 2 {
		t.Fatalf("status = %d calls = %d, want chat_id 0 to set the default", rec.Code, bots.setCalls)
	}
}
