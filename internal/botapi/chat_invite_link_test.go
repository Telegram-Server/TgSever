package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

// chatInviteLinkCall 记录假网关收到的一次邀请链接调用。
type chatInviteLinkCall struct {
	method  string
	botID   int64
	chatID  int64
	link    string
	options domain.BotAPIChatInviteLinkOptions
}

type fakeChatInviteLinkGateway struct {
	*fakeBotAPIGateway
	calls  []chatInviteLinkCall
	result domain.BotAPIChatInviteLink
	export string
	err    error
}

func newFakeChatInviteLinkGateway() *fakeChatInviteLinkGateway {
	return &fakeChatInviteLinkGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func (f *fakeChatInviteLinkGateway) record(call chatInviteLinkCall) (domain.BotAPIChatInviteLink, error) {
	f.calls = append(f.calls, call)
	if f.err != nil {
		return domain.BotAPIChatInviteLink{}, f.err
	}
	return f.result, nil
}

func (f *fakeChatInviteLinkGateway) lastCall(t *testing.T) chatInviteLinkCall {
	t.Helper()
	if len(f.calls) == 0 {
		t.Fatal("gateway was not called")
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeChatInviteLinkGateway) BotAPICreateChatInviteLink(_ context.Context, botID, chatID int64, options domain.BotAPIChatInviteLinkOptions) (domain.BotAPIChatInviteLink, error) {
	return f.record(chatInviteLinkCall{method: "create", botID: botID, chatID: chatID, options: options})
}

func (f *fakeChatInviteLinkGateway) BotAPIEditChatInviteLink(_ context.Context, botID, chatID int64, link string, options domain.BotAPIChatInviteLinkOptions) (domain.BotAPIChatInviteLink, error) {
	return f.record(chatInviteLinkCall{method: "edit", botID: botID, chatID: chatID, link: link, options: options})
}

func (f *fakeChatInviteLinkGateway) BotAPIRevokeChatInviteLink(_ context.Context, botID, chatID int64, link string) (domain.BotAPIChatInviteLink, error) {
	return f.record(chatInviteLinkCall{method: "revoke", botID: botID, chatID: chatID, link: link})
}

func (f *fakeChatInviteLinkGateway) BotAPIExportChatInviteLink(_ context.Context, botID, chatID int64) (string, error) {
	f.calls = append(f.calls, chatInviteLinkCall{method: "export", botID: botID, chatID: chatID})
	return f.export, f.err
}

func inviteOption[T any](v T) *T { return &v }

func TestCreateChatInviteLinkPassesOptionsAndReturnsInviteLink(t *testing.T) {
	gateway := newFakeChatInviteLinkGateway()
	gateway.result = domain.BotAPIChatInviteLink{
		Invite: domain.ChannelInvite{
			Hash: "abc", Title: "promo", ExpireDate: 1_900_000_000, UsageLimit: 50,
		},
		Link:    "https://t.me/+abc",
		Creator: domain.User{ID: 1001, Bot: true, FirstName: "TestBot", Username: "test_bot"},
	}
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "createChatInviteLink",
		`{"chat_id":-1001234,"name":"promo","expire_date":1900000000,"member_limit":50}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("createChatInviteLink = %d %+v", rec.Code, resp)
	}
	call := gateway.lastCall(t)
	want := domain.BotAPIChatInviteLinkOptions{Name: inviteOption("promo"), ExpireDate: inviteOption(1_900_000_000), MemberLimit: inviteOption(50)}
	if call.method != "create" || call.botID != 1001 || call.chatID != -1001234 || !reflect.DeepEqual(call.options, want) {
		t.Fatalf("call = %+v, want create on -1001234 with %+v", call, want)
	}
	var got map[string]any
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	wantJSON := map[string]any{
		"invite_link":          "https://t.me/+abc",
		"creator":              map[string]any{"id": float64(1001), "is_bot": true, "first_name": "TestBot", "username": "test_bot"},
		"creates_join_request": false,
		"is_primary":           false,
		"is_revoked":           false,
		"name":                 "promo",
		"expire_date":          float64(1_900_000_000),
		"member_limit":         float64(50),
	}
	if !reflect.DeepEqual(got, wantJSON) {
		t.Fatalf("ChatInviteLink = %v\nwant %v", got, wantJSON)
	}
}

// 可选字段为零值时官方省略；pending_join_request_count 只在有待审批申请时出现。
func TestChatInviteLinkProjectionOmitsUnsetFields(t *testing.T) {
	got := apiChatInviteLink(domain.BotAPIChatInviteLink{
		Invite:  domain.ChannelInvite{Hash: "p", Permanent: true, Revoked: true},
		Link:    "https://t.me/+p",
		Creator: domain.User{ID: 7, FirstName: "Owner"},
	})
	for _, key := range []string{"name", "expire_date", "member_limit", "pending_join_request_count"} {
		if _, ok := got[key]; ok {
			t.Fatalf("ChatInviteLink has %q = %v, want it omitted", key, got[key])
		}
	}
	if got["is_primary"] != true || got["is_revoked"] != true || got["creates_join_request"] != false {
		t.Fatalf("ChatInviteLink flags = %v", got)
	}

	withRequests := apiChatInviteLink(domain.BotAPIChatInviteLink{
		Invite:  domain.ChannelInvite{Hash: "r", RequestNeeded: true, RequestedCount: 3},
		Link:    "https://t.me/+r",
		Creator: domain.User{ID: 7, FirstName: "Owner"},
	})
	if withRequests["creates_join_request"] != true || withRequests["pending_join_request_count"] != 3 {
		t.Fatalf("ChatInviteLink = %v, want creates_join_request and 3 pending requests", withRequests)
	}
}

func TestEditChatInviteLinkPassesOnlyGivenFields(t *testing.T) {
	gateway := newFakeChatInviteLinkGateway()
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "editChatInviteLink",
		`{"chat_id":-1001234,"invite_link":"https://t.me/+abc","creates_join_request":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("editChatInviteLink = %d %s", rec.Code, rec.Body.String())
	}
	call := gateway.lastCall(t)
	want := domain.BotAPIChatInviteLinkOptions{CreatesJoinRequest: inviteOption(true)}
	if call.method != "edit" || call.link != "https://t.me/+abc" || !reflect.DeepEqual(call.options, want) {
		t.Fatalf("call = %+v, want edit of the link with only creates_join_request", call)
	}
}

func TestRevokeAndExportChatInviteLink(t *testing.T) {
	gateway := newFakeChatInviteLinkGateway()
	gateway.result = domain.BotAPIChatInviteLink{
		Invite: domain.ChannelInvite{Hash: "abc", Revoked: true}, Link: "https://t.me/+abc",
		Creator: domain.User{ID: 1001, Bot: true, FirstName: "TestBot"},
	}
	gateway.export = "https://t.me/+fresh"
	h := newTestHandler(t, gateway)

	rec := callMethod(t, h, "revokeChatInviteLink", `{"chat_id":-1001234,"invite_link":"https://t.me/+abc"}`)
	resp := decodeAPIResponse(t, rec)
	if !resp.OK || gateway.lastCall(t).method != "revoke" || gateway.lastCall(t).link != "https://t.me/+abc" {
		t.Fatalf("revokeChatInviteLink = %+v, call %+v", resp, gateway.lastCall(t))
	}
	var revoked map[string]any
	if err := json.Unmarshal(resp.Result, &revoked); err != nil || revoked["is_revoked"] != true {
		t.Fatalf("revoked link = %s, want is_revoked=true", resp.Result)
	}

	rec = callMethod(t, h, "exportChatInviteLink", `{"chat_id":-1001234}`)
	resp = decodeAPIResponse(t, rec)
	if !resp.OK || string(resp.Result) != `"https://t.me/+fresh"` || gateway.lastCall(t).method != "export" {
		t.Fatalf("exportChatInviteLink = %+v", resp)
	}
}

func TestChatInviteLinkParameterValidation(t *testing.T) {
	long := strings.Repeat("邀", domain.BotAPIInviteLinkNameMaxLength+1)
	for _, tc := range []struct {
		method, body string
	}{
		{"createChatInviteLink", `{"chat_id":-1001234,"name":"` + long + `"}`},
		{"createChatInviteLink", `{"chat_id":-1001234,"member_limit":0}`},
		{"createChatInviteLink", `{"chat_id":-1001234,"member_limit":100000}`},
		{"createChatInviteLink", `{"chat_id":-1001234,"member_limit":5,"creates_join_request":true}`},
		{"editChatInviteLink", `{"chat_id":-1001234,"invite_link":"https://t.me/+abc","name":"` + long + `"}`},
		{"editChatInviteLink", `{"chat_id":-1001234,"invite_link":"https://t.me/+abc","member_limit":-1}`},
		{"editChatInviteLink", `{"chat_id":-1001234,"invite_link":"https://t.me/+abc","member_limit":5,"creates_join_request":true}`},
		{"editChatInviteLink", `{"chat_id":-1001234}`},
		{"revokeChatInviteLink", `{"chat_id":-1001234}`},
	} {
		gateway := newFakeChatInviteLinkGateway()
		rec := callMethod(t, newTestHandler(t, gateway), tc.method, tc.body)
		resp := decodeAPIResponse(t, rec)
		if rec.Code != http.StatusBadRequest || resp.OK || !strings.HasPrefix(resp.Description, "Bad Request: ") {
			t.Fatalf("%s %s = %d %+v, want a Bad Request parameter error", tc.method, tc.body, rec.Code, resp)
		}
		if len(gateway.calls) != 0 {
			t.Fatalf("%s %s reached the gateway: %+v", tc.method, tc.body, gateway.calls)
		}
	}

	// 32 个字符（不是字节）以内的名字合法；member_limit 只在要求审批时互斥。
	gateway := newFakeChatInviteLinkGateway()
	ok := strings.Repeat("邀", domain.BotAPIInviteLinkNameMaxLength)
	rec := callMethod(t, newTestHandler(t, gateway), "createChatInviteLink",
		`{"chat_id":-1001234,"name":"`+ok+`","member_limit":99999,"creates_join_request":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("createChatInviteLink with boundary values = %d %s", rec.Code, rec.Body.String())
	}
}

// 没有邀请权限时四个方法都回官方的权限错误描述。
func TestChatInviteLinkMethodsReportMissingRights(t *testing.T) {
	gateway := newFakeChatInviteLinkGateway()
	gateway.err = errors.New("rpc error code 400: CHAT_ADMIN_REQUIRED")
	h := newTestHandler(t, gateway)
	for _, tc := range []struct {
		method, body string
	}{
		{"createChatInviteLink", `{"chat_id":-1001234}`},
		{"editChatInviteLink", `{"chat_id":-1001234,"invite_link":"https://t.me/+abc","name":"x"}`},
		{"revokeChatInviteLink", `{"chat_id":-1001234,"invite_link":"https://t.me/+abc"}`},
		{"exportChatInviteLink", `{"chat_id":-1001234}`},
	} {
		rec := callMethod(t, h, tc.method, tc.body)
		resp := decodeAPIResponse(t, rec)
		if rec.Code != http.StatusBadRequest || resp.Description != "Bad Request: not enough rights to manage chat invite links" {
			t.Fatalf("%s = %d %q, want the official rights error", tc.method, rec.Code, resp.Description)
		}
	}
}
