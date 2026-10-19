package botapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
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

// menuSet 是网关收到的一次按用户写入。
type menuSet struct {
	userID int64
	button domain.BotMenuButton
}

// recordingMenuGateway 只记录按用户的菜单按钮读写并按 err 失败；按钮的存取语义（default
// 删除专属按钮、缓存失效与推送）属于生产里的 rpc.Router，不在这里模拟。读取返回一个 URL
// 带用户 id 的按钮，以证明 chat_id 原样到达网关。
type recordingMenuGateway struct {
	*fakeBotAPIGateway
	sets []menuSet
	gets []int64
	err  error
}

func (g *recordingMenuGateway) BotAPISetChatMenuButton(_ context.Context, _ int64, userID int64, button domain.BotMenuButton) error {
	g.sets = append(g.sets, menuSet{userID: userID, button: button})
	return g.err
}

func (g *recordingMenuGateway) BotAPIGetChatMenuButton(_ context.Context, _ int64, userID int64) (domain.BotMenuButton, error) {
	g.gets = append(g.gets, userID)
	if g.err != nil {
		return domain.BotMenuButton{}, g.err
	}
	return domain.BotMenuButton{Type: domain.BotMenuButtonWebView, Text: "Mine", URL: "https://example.com/" + strconv.FormatInt(userID, 10)}, nil
}

func newMenuButtonHandler() (http.Handler, *recordingMenuBots, *recordingMenuGateway) {
	bots := &recordingMenuBots{fakeBotAPIBots: &fakeBotAPIBots{profile: testBotProfile}}
	gateway := &recordingMenuGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
	return (&handler{bots: bots, gateway: gateway}).routes(), bots, gateway
}

// 正数 chat_id 是私聊对方的用户 id：读写都按该用户转发给网关，不碰全局默认。
func TestChatMenuButtonPositiveChatIDForwardsPerUser(t *testing.T) {
	h, bots, gateway := newMenuButtonHandler()

	rec := callMethod(t, h, "setChatMenuButton", `{"chat_id":42,"menu_button":{"type":"web_app","text":"Mine","web_app":{"url":"https://example.com/42"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(gateway.sets) != 1 || gateway.sets[0].userID != 42 || gateway.sets[0].button.URL != "https://example.com/42" {
		t.Fatalf("gateway sets = %+v, want the web_app button for user 42", gateway.sets)
	}
	rec = callMethod(t, h, "getChatMenuButton", `{"chat_id":"43"}`)
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, "https://example.com/43") {
		t.Fatalf("get user 43 = %d %s, want the gateway's button for user 43", rec.Code, body)
	}
	if bots.setCalls != 0 {
		t.Fatalf("per-chat set touched the default button (%d calls)", bots.setCalls)
	}
}

// 不带 chat_id 或 chat_id 为 0 表示全局默认，同样经网关（用户 0）写入，由网关负责缓存失效。
func TestChatMenuButtonDefaultGoesThroughGatewayAsUserZero(t *testing.T) {
	h, bots, gateway := newMenuButtonHandler()

	for _, body := range []string{
		`{"menu_button":{"type":"commands"}}`,
		`{"chat_id":0,"menu_button":{"type":"commands"}}`,
	} {
		if rec := callMethod(t, h, "setChatMenuButton", body); rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body = %s", body, rec.Code, rec.Body.String())
		}
	}
	if len(gateway.sets) != 2 || gateway.sets[0].userID != 0 || gateway.sets[1].userID != 0 ||
		gateway.sets[0].button.Type != domain.BotMenuButtonCommands {
		t.Fatalf("gateway sets = %+v, want two commands buttons for user 0", gateway.sets)
	}
	if bots.setCalls != 0 {
		t.Fatalf("default set bypassed the gateway (%d direct calls)", bots.setCalls)
	}
}

// 群/频道（负数）、@username 与乱码都不是私聊：报 CHAT_ID_INVALID，不碰网关也不碰全局默认。
func TestChatMenuButtonRejectsNonPrivateChatIDs(t *testing.T) {
	h, bots, gateway := newMenuButtonHandler()

	for _, chatID := range []string{`-1001234`, `"@somegroup"`, `"abc"`} {
		for _, method := range []string{"setChatMenuButton", "getChatMenuButton"} {
			rec := callMethod(t, h, method, `{"chat_id":`+chatID+`,"menu_button":{"type":"commands"}}`)
			if rec.Code != http.StatusBadRequest || decodeAPIResponse(t, rec).Description != "CHAT_ID_INVALID" {
				t.Fatalf("%s(%s): status = %d body = %s, want 400 CHAT_ID_INVALID", method, chatID, rec.Code, rec.Body.String())
			}
		}
	}
	if len(gateway.sets) != 0 || len(gateway.gets) != 0 || bots.setCalls != 0 {
		t.Fatalf("rejected calls reached a store: gateway sets %+v gets %v, default sets %d", gateway.sets, gateway.gets, bots.setCalls)
	}
}

// 网关的拒绝（如未知用户）原样报出。
func TestChatMenuButtonSurfacesGatewayErrors(t *testing.T) {
	h, _, gateway := newMenuButtonHandler()
	gateway.err = errors.New("USER_ID_INVALID")

	for _, method := range []string{"setChatMenuButton", "getChatMenuButton"} {
		rec := callMethod(t, h, method, `{"chat_id":404,"menu_button":{"type":"commands"}}`)
		if rec.Code != http.StatusBadRequest || decodeAPIResponse(t, rec).Description != "USER_ID_INVALID" {
			t.Fatalf("%s: status = %d body = %s, want 400 USER_ID_INVALID", method, rec.Code, rec.Body.String())
		}
	}
}

// 网关不支持菜单按钮时，全局默认仍可直接设置。
func TestChatMenuButtonDefaultWithoutGateway(t *testing.T) {
	bots := &recordingMenuBots{fakeBotAPIBots: &fakeBotAPIBots{profile: testBotProfile}}
	h := (&handler{bots: bots, gateway: &fakeBotAPIGateway{}}).routes()

	rec := callMethod(t, h, "setChatMenuButton", `{"menu_button":{"type":"commands"}}`)
	if rec.Code != http.StatusOK || bots.setCalls != 1 || bots.set.Type != domain.BotMenuButtonCommands {
		t.Fatalf("status = %d calls = %d, want the default button set", rec.Code, bots.setCalls)
	}
	rec = callMethod(t, h, "setChatMenuButton", `{"chat_id":0,"menu_button":{"type":"default"}}`)
	if rec.Code != http.StatusOK || bots.setCalls != 2 {
		t.Fatalf("status = %d calls = %d, want chat_id 0 to set the default", rec.Code, bots.setCalls)
	}
}
