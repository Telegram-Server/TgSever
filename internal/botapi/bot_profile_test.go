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

func TestSetMyNameStoresName(t *testing.T) {
	gateway := newFakeBotProfileGateway()
	h := newTestHandler(t, gateway)

	resp := decodeAPIResponse(t, callMethod(t, h, "setMyName", `{"name":"Helper"}`))
	if !resp.OK || string(resp.Result) != "true" {
		t.Fatalf("setMyName = %+v", resp)
	}
	if gateway.name != "Helper" || gateway.lastBotID != testBotProfile.BotUserID {
		t.Fatalf("gateway name = %q for bot %d", gateway.name, gateway.lastBotID)
	}
}

func TestSetMyNameRejectsTooLongNameBeforeGateway(t *testing.T) {
	gateway := newFakeBotProfileGateway()
	h := newTestHandler(t, gateway)

	long := strings.Repeat("я", 65)
	rec := callMethod(t, h, "setMyName", `{"name":"`+long+`"}`)
	resp := decodeAPIResponse(t, rec)
	if resp.OK || rec.Code != http.StatusBadRequest || resp.Description != "NAME_TOO_LONG" {
		t.Fatalf("setMyName(65) = %d %+v", rec.Code, resp)
	}
	if gateway.setCalls != 0 {
		t.Fatalf("gateway called %d times", gateway.setCalls)
	}
	if resp := decodeAPIResponse(t, callMethod(t, h, "setMyName", `{"name":"`+long[2:]+`"}`)); !resp.OK {
		t.Fatalf("setMyName(64) = %+v", resp)
	}
}

func TestSetMyNameRejectsLanguageCode(t *testing.T) {
	gateway := newFakeBotProfileGateway()
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "setMyName", `{"name":"Помощник","language_code":"ru"}`)
	resp := decodeAPIResponse(t, rec)
	if resp.OK || rec.Code != http.StatusBadRequest || resp.Description != "LANG_CODE_NOT_SUPPORTED" {
		t.Fatalf("setMyName(ru) = %d %+v", rec.Code, resp)
	}
	if gateway.setCalls != 0 {
		t.Fatalf("gateway called %d times", gateway.setCalls)
	}
}

func TestSetMyDescriptionStoresDescriptionAndChecksLength(t *testing.T) {
	gateway := newFakeBotProfileGateway()
	h := newTestHandler(t, gateway)

	if resp := decodeAPIResponse(t, callMethod(t, h, "setMyDescription", `{"description":"What I do"}`)); !resp.OK {
		t.Fatalf("setMyDescription = %+v", resp)
	}
	if gateway.description != "What I do" || gateway.name != "" {
		t.Fatalf("gateway description = %q, name = %q", gateway.description, gateway.name)
	}
	rec := callMethod(t, h, "setMyDescription", `{"description":"`+strings.Repeat("x", 513)+`"}`)
	if resp := decodeAPIResponse(t, rec); resp.OK || resp.Description != "DESCRIPTION_TOO_LONG" {
		t.Fatalf("setMyDescription(513) = %+v", resp)
	}
	rec = callMethod(t, h, "setMyDescription", `{"description":"x","language_code":"en"}`)
	if resp := decodeAPIResponse(t, rec); resp.OK || resp.Description != "LANG_CODE_NOT_SUPPORTED" {
		t.Fatalf("setMyDescription(en) = %+v", resp)
	}
	if gateway.setCalls != 1 {
		t.Fatalf("gateway called %d times, want 1", gateway.setCalls)
	}
}

func TestGetMyDescriptionReturnsBotDescription(t *testing.T) {
	gateway := newFakeBotProfileGateway()
	gateway.name, gateway.about, gateway.description = "Helper", "short", "long"
	h := newTestHandler(t, gateway)

	resp := decodeAPIResponse(t, callMethod(t, h, "getMyDescription", `{}`))
	if !resp.OK || string(resp.Result) != `{"description":"long"}` {
		t.Fatalf("getMyDescription = %+v (%s)", resp, resp.Result)
	}
}

func TestSetMyShortDescriptionStoresAboutAndChecksLength(t *testing.T) {
	gateway := newFakeBotProfileGateway()
	h := newTestHandler(t, gateway)

	if resp := decodeAPIResponse(t, callMethod(t, h, "setMyShortDescription", `{"short_description":"Hi"}`)); !resp.OK {
		t.Fatalf("setMyShortDescription = %+v", resp)
	}
	if gateway.about != "Hi" || gateway.description != "" {
		t.Fatalf("gateway about = %q, description = %q", gateway.about, gateway.description)
	}
	rec := callMethod(t, h, "setMyShortDescription", `{"short_description":"`+strings.Repeat("x", 121)+`"}`)
	if resp := decodeAPIResponse(t, rec); resp.OK || resp.Description != "ABOUT_TOO_LONG" {
		t.Fatalf("setMyShortDescription(121) = %+v", resp)
	}
	if gateway.setCalls != 1 {
		t.Fatalf("gateway called %d times, want 1", gateway.setCalls)
	}
}

func TestGetMyShortDescriptionReturnsAbout(t *testing.T) {
	gateway := newFakeBotProfileGateway()
	gateway.name, gateway.about, gateway.description = "Helper", "short", "long"
	h := newTestHandler(t, gateway)

	resp := decodeAPIResponse(t, callMethod(t, h, "getMyShortDescription", `{}`))
	if !resp.OK || string(resp.Result) != `{"short_description":"short"}` {
		t.Fatalf("getMyShortDescription = %+v (%s)", resp, resp.Result)
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

func TestGetMyNameReturnsBotName(t *testing.T) {
	gateway := newFakeBotProfileGateway()
	gateway.name = "Helper"
	h := newTestHandler(t, gateway)

	resp := decodeAPIResponse(t, callMethod(t, h, "getMyName", `{"language_code":"de"}`))
	if !resp.OK || string(resp.Result) != `{"name":"Helper"}` {
		t.Fatalf("getMyName = %+v (%s)", resp, resp.Result)
	}
	if gateway.lastLanguageCode != "de" {
		t.Fatalf("language_code passed = %q, want de", gateway.lastLanguageCode)
	}
}
