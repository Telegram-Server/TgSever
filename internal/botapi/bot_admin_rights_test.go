package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"telesrv/internal/domain"
)

type defaultAdminRightsCall struct {
	botID       int64
	forChannels bool
	rights      domain.ChannelAdminRights
}

type fakeDefaultAdminRightsGateway struct {
	*fakeBotAPIGateway
	sets   []defaultAdminRightsCall
	gets   []defaultAdminRightsCall
	stored domain.ChannelAdminRights
	err    error
}

func (f *fakeDefaultAdminRightsGateway) BotAPISetMyDefaultAdministratorRights(_ context.Context, botID int64, forChannels bool, rights domain.ChannelAdminRights) error {
	f.sets = append(f.sets, defaultAdminRightsCall{botID: botID, forChannels: forChannels, rights: rights})
	return f.err
}

func (f *fakeDefaultAdminRightsGateway) BotAPIGetMyDefaultAdministratorRights(_ context.Context, botID int64, forChannels bool) (domain.ChannelAdminRights, error) {
	f.gets = append(f.gets, defaultAdminRightsCall{botID: botID, forChannels: forChannels})
	return f.stored, f.err
}

func newFakeDefaultAdminRightsGateway() *fakeDefaultAdminRightsGateway {
	return &fakeDefaultAdminRightsGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func TestSetMyDefaultAdministratorRightsParsesRights(t *testing.T) {
	gateway := newFakeDefaultAdminRightsGateway()
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "setMyDefaultAdministratorRights", `{"rights":{"can_manage_chat":true,"can_delete_messages":true,"can_invite_users":true,"can_post_messages":false,"is_anonymous":true},"for_channels":true}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK || string(resp.Result) != "true" {
		t.Fatalf("set response = %d %+v", rec.Code, resp)
	}
	want := domain.ChannelAdminRights{ManageChat: true, DeleteMessages: true, InviteUsers: true, Anonymous: true}
	if len(gateway.sets) != 1 || gateway.sets[0] != (defaultAdminRightsCall{botID: testBotProfile.BotUserID, forChannels: true, rights: want}) {
		t.Fatalf("gateway calls = %+v, want one channels call with %+v", gateway.sets, want)
	}
}

// 不传 rights 就是清空；不传 for_channels 是群那组。
func TestSetMyDefaultAdministratorRightsWithoutRightsClears(t *testing.T) {
	gateway := newFakeDefaultAdminRightsGateway()
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "setMyDefaultAdministratorRights", `{}`)
	if resp := decodeAPIResponse(t, rec); !resp.OK {
		t.Fatalf("set without rights = %+v", resp)
	}
	if len(gateway.sets) != 1 || gateway.sets[0] != (defaultAdminRightsCall{botID: testBotProfile.BotUserID}) {
		t.Fatalf("gateway calls = %+v, want zero group rights", gateway.sets)
	}
}

func TestSetMyDefaultAdministratorRightsRejectsBadRights(t *testing.T) {
	gateway := newFakeDefaultAdminRightsGateway()
	h := newTestHandler(t, gateway)

	for _, body := range []string{`{"rights":"not json"}`, `{"rights":{"can_manage_chat":"yes"}}`} {
		rec := callMethod(t, h, "setMyDefaultAdministratorRights", body)
		resp := decodeAPIResponse(t, rec)
		if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != "BAD_REQUEST" {
			t.Fatalf("%s -> %d %+v, want 400 BAD_REQUEST", body, rec.Code, resp)
		}
	}
	if len(gateway.sets) != 0 {
		t.Fatalf("gateway called with bad rights: %+v", gateway.sets)
	}
}

func TestSetMyDefaultAdministratorRightsGatewayError(t *testing.T) {
	gateway := newFakeDefaultAdminRightsGateway()
	gateway.err = errors.New("BOT_INVALID")
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "setMyDefaultAdministratorRights", `{"rights":{"can_pin_messages":true}}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != "BOT_INVALID" {
		t.Fatalf("response = %d %+v, want 400 BOT_INVALID", rec.Code, resp)
	}
}

// 返回完整的 ChatAdministratorRights：未设置时每个字段都在且为 false。
func TestGetMyDefaultAdministratorRightsShape(t *testing.T) {
	gateway := newFakeDefaultAdminRightsGateway()
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "getMyDefaultAdministratorRights", `{}`)
	resp := decodeAPIResponse(t, rec)
	if !resp.OK {
		t.Fatalf("get response = %+v", resp)
	}
	var unset map[string]bool
	if err := json.Unmarshal(resp.Result, &unset); err != nil {
		t.Fatalf("decode rights %s: %v", resp.Result, err)
	}
	fields := []string{
		"is_anonymous", "can_manage_chat", "can_delete_messages", "can_manage_video_chats",
		"can_restrict_members", "can_promote_members", "can_change_info", "can_invite_users",
		"can_post_stories", "can_edit_stories", "can_delete_stories", "can_post_messages",
		"can_edit_messages", "can_pin_messages", "can_manage_topics",
	}
	for _, field := range fields {
		if v, ok := unset[field]; !ok || v {
			t.Fatalf("unset rights %s = %v (present=%v), want false; got %s", field, v, ok, resp.Result)
		}
	}
	if len(gateway.gets) != 1 || gateway.gets[0].forChannels {
		t.Fatalf("gateway gets = %+v, want one group read", gateway.gets)
	}

	gateway.stored = domain.ChannelAdminRights{PostMessages: true, ManageChat: true}
	rec = callMethod(t, h, "getMyDefaultAdministratorRights", `{"for_channels":true}`)
	resp = decodeAPIResponse(t, rec)
	var set map[string]bool
	if err := json.Unmarshal(resp.Result, &set); err != nil {
		t.Fatalf("decode rights %s: %v", resp.Result, err)
	}
	if !set["can_post_messages"] || !set["can_manage_chat"] || set["can_delete_messages"] {
		t.Fatalf("channel rights = %s", resp.Result)
	}
	if !gateway.gets[1].forChannels {
		t.Fatal("for_channels=true not passed to the gateway")
	}
}
