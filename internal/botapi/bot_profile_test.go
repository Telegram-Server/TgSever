package botapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

// fakeBotProfileGateway 在默认假网关上补 BotProfileGateway，资料存在内存里。
type fakeBotProfileGateway struct {
	*fakeBotAPIGateway
	name, about, description string
	setCalls                 int
	lastBotID                int64
	lastLanguageCode         string
}

func newFakeBotProfileGateway() *fakeBotProfileGateway {
	return &fakeBotProfileGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func (g *fakeBotProfileGateway) BotAPISetBotInfo(_ context.Context, botID int64, languageCode string, upd domain.BotInfoUpdate) (bool, error) {
	g.setCalls++
	g.lastBotID, g.lastLanguageCode = botID, languageCode
	if upd.SetName {
		g.name = upd.Name
	}
	if upd.SetAbout {
		g.about = upd.About
	}
	if upd.SetDescription {
		g.description = upd.Description
	}
	return true, nil
}

func (g *fakeBotProfileGateway) BotAPIGetBotInfo(_ context.Context, botID int64, languageCode string) (string, string, string, error) {
	g.lastBotID, g.lastLanguageCode = botID, languageCode
	return g.name, g.about, g.description, nil
}

// 每个 setter 只写自己那一项（长度上限按字符计，边界值可写入），其余两项不动。
func TestBotProfileSettersStoreOnlyTheirField(t *testing.T) {
	for _, tc := range []struct {
		method, param, value string
		// stored 依次是 name、about、description 应有的值。
		stored [3]string
	}{
		{"setMyName", "name", strings.Repeat("я", 64), [3]string{strings.Repeat("я", 64), "", ""}},
		{"setMyShortDescription", "short_description", strings.Repeat("я", 120), [3]string{"", strings.Repeat("я", 120), ""}},
		{"setMyDescription", "description", strings.Repeat("я", 512), [3]string{"", "", strings.Repeat("я", 512)}},
	} {
		t.Run(tc.method, func(t *testing.T) {
			gateway := newFakeBotProfileGateway()
			resp := decodeAPIResponse(t, callMethod(t, newTestHandler(t, gateway), tc.method, `{"`+tc.param+`":"`+tc.value+`"}`))
			if !resp.OK || string(resp.Result) != "true" {
				t.Fatalf("%s = %+v", tc.method, resp)
			}
			if got := [3]string{gateway.name, gateway.about, gateway.description}; got != tc.stored || gateway.lastBotID != testBotProfile.BotUserID {
				t.Fatalf("stored (name, about, description) = %q for bot %d, want %q", got, gateway.lastBotID, tc.stored)
			}
		})
	}
}

// 超长或带 language_code 的写入在到达网关前就以 400 拒绝。
func TestBotProfileSettersRejectBeforeGateway(t *testing.T) {
	for _, tc := range []struct {
		name, method, body, code string
	}{
		{"long name", "setMyName", `{"name":"` + strings.Repeat("я", 65) + `"}`, "NAME_TOO_LONG"},
		{"long short description", "setMyShortDescription", `{"short_description":"` + strings.Repeat("я", 121) + `"}`, "ABOUT_TOO_LONG"},
		{"long description", "setMyDescription", `{"description":"` + strings.Repeat("я", 513) + `"}`, "DESCRIPTION_TOO_LONG"},
		{"name language_code", "setMyName", `{"name":"Помощник","language_code":"ru"}`, "LANG_CODE_NOT_SUPPORTED"},
		{"description language_code", "setMyDescription", `{"description":"x","language_code":"en"}`, "LANG_CODE_NOT_SUPPORTED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := newFakeBotProfileGateway()
			rec := callMethod(t, newTestHandler(t, gateway), tc.method, tc.body)
			if resp := decodeAPIResponse(t, rec); resp.OK || rec.Code != http.StatusBadRequest || resp.Description != tc.code {
				t.Fatalf("%s = %d %+v, want 400 %s", tc.method, rec.Code, resp, tc.code)
			}
			if gateway.setCalls != 0 {
				t.Fatalf("gateway called %d times", gateway.setCalls)
			}
		})
	}
}

// 每个 getter 只取自己那一项；language_code 原样传给网关。
func TestBotProfileGettersReturnTheirField(t *testing.T) {
	for _, tc := range []struct {
		method, want string
	}{
		{"getMyName", `{"name":"Helper"}`},
		{"getMyShortDescription", `{"short_description":"short"}`},
		{"getMyDescription", `{"description":"long"}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			gateway := newFakeBotProfileGateway()
			gateway.name, gateway.about, gateway.description = "Helper", "short", "long"
			resp := decodeAPIResponse(t, callMethod(t, newTestHandler(t, gateway), tc.method, `{"language_code":"de"}`))
			if !resp.OK || string(resp.Result) != tc.want {
				t.Fatalf("%s = %+v (%s), want %s", tc.method, resp, resp.Result, tc.want)
			}
			if gateway.lastLanguageCode != "de" {
				t.Fatalf("language_code passed = %q, want de", gateway.lastLanguageCode)
			}
		})
	}
}

func TestBotProfileMethodsNeedGatewaySupport(t *testing.T) {
	h := newTestHandler(t, &fakeBotAPIGateway{})

	for _, method := range []string{
		"setMyName", "getMyName", "setMyDescription", "getMyDescription",
		"setMyShortDescription", "getMyShortDescription",
	} {
		rec := callMethod(t, h, method, `{}`)
		if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusNotImplemented || resp.Description != "METHOD_NOT_FOUND" {
			t.Fatalf("%s without gateway = %d %+v", method, rec.Code, resp)
		}
	}
}
