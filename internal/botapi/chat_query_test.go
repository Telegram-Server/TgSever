package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"telesrv/internal/domain"
)

// fakeChatQueryGateway 记录查询类方法收到的参数，并返回预置的结果。
type fakeChatQueryGateway struct {
	*fakeBotAPIGateway
	gotBotID  int64
	gotChatID int64
	gotUserID int64
	err       error

	memberCount int

	member      domain.ChannelMember
	memberUser  domain.User
	canBeEdited bool

	admins        []domain.ChannelMember
	adminUsers    []domain.User
	adminEditable map[int64]bool

	chatUser    domain.User
	chatChannel domain.ChannelView
}

func (f *fakeChatQueryGateway) BotAPIGetChatUser(_ context.Context, botID, userID int64) (domain.User, error) {
	f.gotBotID, f.gotUserID = botID, userID
	return f.chatUser, f.err
}

func (f *fakeChatQueryGateway) BotAPIGetChatChannel(_ context.Context, botID, chatID int64) (domain.ChannelView, error) {
	f.gotBotID, f.gotChatID = botID, chatID
	return f.chatChannel, f.err
}

func getChatResult(t *testing.T, h http.Handler, body string) map[string]any {
	t.Helper()
	rec := callMethod(t, h, "getChat", body)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("%s: status = %d body = %s", body, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return out
}

func TestGetChatPrivateReturnsUserInfo(t *testing.T) {
	gateway := newFakeChatQueryGateway()
	gateway.chatUser = domain.User{ID: 7, FirstName: "Ann", LastName: "Lee", Username: "ann", About: "hello there",
		Birthday: domain.Birthday{Day: 3, Month: 4}}
	h := newTestHandler(t, gateway)

	got := getChatResult(t, h, `{"chat_id":7}`)
	if got["id"] != float64(7) || got["type"] != "private" || got["first_name"] != "Ann" || got["last_name"] != "Lee" ||
		got["username"] != "ann" || got["bio"] != "hello there" || got["accent_color_id"] != float64(0) ||
		got["max_reaction_count"] != float64(11) {
		t.Fatalf("result = %v", got)
	}
	if _, ok := got["accepted_gift_types"].(map[string]any); !ok {
		t.Fatalf("accepted_gift_types missing: %v", got)
	}
	if birth, _ := got["birthdate"].(map[string]any); birth["day"] != float64(3) || birth["month"] != float64(4) || birth["year"] != nil {
		t.Fatalf("birthdate = %v", got["birthdate"])
	}
	for _, key := range []string{"title", "description", "permissions"} {
		if _, ok := got[key]; ok {
			t.Fatalf("private chat carries %q: %v", key, got)
		}
	}
	if gateway.gotUserID != 7 {
		t.Fatalf("gateway got user %d", gateway.gotUserID)
	}
}

func TestGetChatSupergroupReturnsGroupInfo(t *testing.T) {
	gateway := newFakeChatQueryGateway()
	gateway.chatChannel = domain.ChannelView{Channel: domain.Channel{
		ID: 42, Title: "Bridge Group", Username: "bridgegroup", About: "the group", Megagroup: true,
		SlowmodeSeconds: 30, NoForwards: true, JoinToSend: true, JoinRequest: true,
		DefaultBannedRights: domain.ChannelBannedRights{SendPolls: true},
		Color:               domain.ChannelPeerColor{HasColor: true, Color: 5},
		ReactionPolicy:      domain.ChannelReactionPolicy{Limit: 3},
	}}
	h := newTestHandler(t, gateway)

	got := getChatResult(t, h, `{"chat_id":-1000000000042}`)
	perms, _ := got["permissions"].(map[string]any)
	if got["id"] != float64(-1000000000042) || got["type"] != "supergroup" || got["title"] != "Bridge Group" ||
		got["username"] != "bridgegroup" || got["description"] != "the group" || got["slow_mode_delay"] != float64(30) ||
		got["has_protected_content"] != true || got["join_to_send_messages"] != true || got["join_by_request"] != true ||
		got["accent_color_id"] != float64(5) || got["max_reaction_count"] != float64(3) ||
		perms["can_send_polls"] != false || perms["can_send_messages"] != true {
		t.Fatalf("result = %v", got)
	}
	if _, ok := got["first_name"]; ok {
		t.Fatalf("group carries first_name: %v", got)
	}
	if gateway.gotChatID != -1000000000042 {
		t.Fatalf("gateway got chat %d", gateway.gotChatID)
	}
}

// 广播频道要报 channel：没有默认成员权限，也没有慢速模式。
func TestGetChatBroadcastChannelIsChannel(t *testing.T) {
	gateway := newFakeChatQueryGateway()
	gateway.chatChannel = domain.ChannelView{Channel: domain.Channel{ID: 43, Title: "News", Broadcast: true}}
	h := newTestHandler(t, gateway)

	got := getChatResult(t, h, `{"chat_id":-1000000000043}`)
	if got["type"] != "channel" || got["title"] != "News" || got["accent_color_id"] != float64(0) ||
		got["max_reaction_count"] != float64(11) {
		t.Fatalf("result = %v", got)
	}
	for _, key := range []string{"permissions", "slow_mode_delay", "description", "username", "has_protected_content"} {
		if _, ok := got[key]; ok {
			t.Fatalf("channel carries %q: %v", key, got)
		}
	}
}

// 本服没有旧式普通群：-5 这类 chat_id 直接拒绝，不去问网关。
func TestGetChatRejectsBasicGroupIDs(t *testing.T) {
	gateway := newFakeChatQueryGateway()
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "getChat", `{"chat_id":-5}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != "CHAT_ID_INVALID" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if gateway.gotChatID != 0 || gateway.gotUserID != 0 {
		t.Fatal("gateway was asked about a basic-group chat id")
	}
}

func (f *fakeChatQueryGateway) BotAPIGetChatAdministrators(_ context.Context, botID, chatID int64) ([]domain.ChannelMember, []domain.User, map[int64]bool, error) {
	f.gotBotID, f.gotChatID = botID, chatID
	return f.admins, f.adminUsers, f.adminEditable, f.err
}

func TestGetChatAdministratorsOmitsOtherBotsByDefault(t *testing.T) {
	gateway := newFakeChatQueryGateway()
	self := testBotProfile.BotUserID
	gateway.admins = []domain.ChannelMember{
		{UserID: 5, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive},
		{UserID: 6, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive},
		{UserID: 9, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive},
		{UserID: self, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive},
	}
	gateway.adminUsers = []domain.User{
		{ID: 5, FirstName: "Owner"},
		{ID: 6, FirstName: "Mod"},
		{ID: 9, FirstName: "OtherBot", Bot: true},
		{ID: self, FirstName: "Me", Bot: true},
	}
	gateway.adminEditable = map[int64]bool{6: true}
	h := newTestHandler(t, gateway)

	decode := func(body string) []map[string]any {
		t.Helper()
		rec := callMethod(t, h, "getChatAdministrators", body)
		resp := decodeAPIResponse(t, rec)
		if rec.Code != http.StatusOK || !resp.OK {
			t.Fatalf("%s: status = %d body = %s", body, rec.Code, rec.Body.String())
		}
		var out []map[string]any
		if err := json.Unmarshal(resp.Result, &out); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		return out
	}
	ids := func(list []map[string]any) []float64 {
		var out []float64
		for _, item := range list {
			user, _ := item["user"].(map[string]any)
			id, _ := user["id"].(float64)
			out = append(out, id)
		}
		return out
	}

	got := decode(`{"chat_id":-1000000000042}`)
	if want := []float64{5, 6, float64(self)}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("admins = %v, want %v (other bots omitted)", ids(got), want)
	}
	if got[0]["status"] != "creator" || got[1]["status"] != "administrator" || got[1]["can_be_edited"] != true ||
		got[2]["can_be_edited"] != false {
		t.Fatalf("result = %v", got)
	}
	got = decode(`{"chat_id":-1000000000042,"return_bots":true}`)
	if want := []float64{5, 6, 9, float64(self)}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("admins with return_bots = %v, want %v", ids(got), want)
	}
}

func (f *fakeChatQueryGateway) BotAPIGetChatMember(_ context.Context, botID, chatID, userID int64) (domain.ChannelMember, domain.User, bool, error) {
	f.gotBotID, f.gotChatID, f.gotUserID = botID, chatID, userID
	return f.member, f.memberUser, f.canBeEdited, f.err
}

func TestGetChatMemberProjectsChatMember(t *testing.T) {
	gateway := newFakeChatQueryGateway()
	gateway.member = domain.ChannelMember{UserID: 7, Role: domain.ChannelRoleAdmin, Status: domain.ChannelMemberActive,
		AdminRights: domain.ChannelAdminRights{DeleteMessages: true}, Rank: "mod"}
	gateway.memberUser = domain.User{ID: 7, FirstName: "Ann"}
	gateway.canBeEdited = true
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "getChatMember", `{"chat_id":-1000000000042,"user_id":7}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	user, _ := got["user"].(map[string]any)
	if got["status"] != "administrator" || got["can_be_edited"] != true || got["can_delete_messages"] != true ||
		got["custom_title"] != "mod" || user["id"] != float64(7) {
		t.Fatalf("result = %v", got)
	}
	if gateway.gotChatID != -1000000000042 || gateway.gotUserID != 7 || gateway.gotBotID != testBotProfile.BotUserID {
		t.Fatalf("gateway got bot %d chat %d user %d", gateway.gotBotID, gateway.gotChatID, gateway.gotUserID)
	}
}

func TestGetChatMemberValidatesParameters(t *testing.T) {
	gateway := newFakeChatQueryGateway()
	h := newTestHandler(t, gateway)

	for body, want := range map[string]string{
		`{"chat_id":-1000000000042}`:              "USER_ID_INVALID",
		`{"chat_id":-1000000000042,"user_id":0}`:  "USER_ID_INVALID",
		`{"chat_id":-1000000000042,"user_id":-5}`: "USER_ID_INVALID",
		`{"user_id":7}`:                           "CHAT_ID_INVALID",
	} {
		rec := callMethod(t, h, "getChatMember", body)
		if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != want {
			t.Fatalf("%s: status = %d body = %s, want %s", body, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestGetChatMemberPassesGatewayErrors(t *testing.T) {
	gateway := newFakeChatQueryGateway()
	gateway.err = errors.New("USER_NOT_PARTICIPANT")
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "getChatMember", `{"chat_id":-1000000000042,"user_id":7}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusBadRequest || resp.Description != "USER_NOT_PARTICIPANT" {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func newFakeChatQueryGateway() *fakeChatQueryGateway {
	return &fakeChatQueryGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func (f *fakeChatQueryGateway) BotAPIGetChatMemberCount(_ context.Context, botID, chatID int64) (int, error) {
	f.gotBotID, f.gotChatID = botID, chatID
	return f.memberCount, f.err
}

func TestChatQueryMethodsNeedTheOptionalGateway(t *testing.T) {
	h := newTestHandler(t, &fakeBotAPIGateway{})
	for _, method := range []string{"getChat", "getChatMember", "getChatAdministrators", "getChatMemberCount"} {
		rec := callMethod(t, h, method, `{"chat_id":-1000000000042,"user_id":7}`)
		if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusNotImplemented || resp.Description != "METHOD_NOT_FOUND" {
			t.Fatalf("%s: status = %d body = %s, want 501 METHOD_NOT_FOUND", method, rec.Code, rec.Body.String())
		}
	}
}

func TestGetChatMemberCountReturnsInteger(t *testing.T) {
	gateway := newFakeChatQueryGateway()
	gateway.memberCount = 3
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "getChatMemberCount", `{"chat_id":-1000000000042}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var count int
	if err := json.Unmarshal(resp.Result, &count); err != nil || count != 3 {
		t.Fatalf("result = %s, want 3", resp.Result)
	}
	if gateway.gotBotID != testBotProfile.BotUserID || gateway.gotChatID != -1000000000042 {
		t.Fatalf("gateway got bot %d chat %d", gateway.gotBotID, gateway.gotChatID)
	}
}
