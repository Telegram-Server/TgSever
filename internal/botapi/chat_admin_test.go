package botapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// chatAdminCall 记录假网关收到的一次群管理调用。
type chatAdminCall struct {
	method    string
	botID     int64
	chatID    int64
	userID    int64
	untilDate int
	revoke    bool
	flag      bool // only_if_banned / disable_notification
	banned    domain.ChannelBannedRights
	admin     domain.ChannelAdminRights
	text      string
	messageID int
	data      []byte
}

type fakeChatAdminGateway struct {
	*fakeBotAPIGateway
	calls []chatAdminCall
	err   error
}

func newFakeChatAdminGateway() *fakeChatAdminGateway {
	return &fakeChatAdminGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func (f *fakeChatAdminGateway) record(call chatAdminCall) error {
	f.calls = append(f.calls, call)
	return f.err
}

func (f *fakeChatAdminGateway) lastCall(t *testing.T) chatAdminCall {
	t.Helper()
	if len(f.calls) == 0 {
		t.Fatal("gateway was not called")
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeChatAdminGateway) BotAPIBanChatMember(_ context.Context, botID, chatID, userID int64, untilDate int, revokeMessages bool) error {
	return f.record(chatAdminCall{method: "ban", botID: botID, chatID: chatID, userID: userID, untilDate: untilDate, revoke: revokeMessages})
}

func (f *fakeChatAdminGateway) BotAPIUnbanChatMember(_ context.Context, botID, chatID, userID int64, onlyIfBanned bool) error {
	return f.record(chatAdminCall{method: "unban", botID: botID, chatID: chatID, userID: userID, flag: onlyIfBanned})
}

func (f *fakeChatAdminGateway) BotAPIRestrictChatMember(_ context.Context, botID, chatID, userID int64, rights domain.ChannelBannedRights) error {
	return f.record(chatAdminCall{method: "restrict", botID: botID, chatID: chatID, userID: userID, banned: rights})
}

func (f *fakeChatAdminGateway) BotAPIPromoteChatMember(_ context.Context, botID, chatID, userID int64, rights domain.ChannelAdminRights) error {
	return f.record(chatAdminCall{method: "promote", botID: botID, chatID: chatID, userID: userID, admin: rights})
}

func (f *fakeChatAdminGateway) BotAPISetChatAdministratorCustomTitle(_ context.Context, botID, chatID, userID int64, title string) error {
	return f.record(chatAdminCall{method: "title", botID: botID, chatID: chatID, userID: userID, text: title})
}

func (f *fakeChatAdminGateway) BotAPISetChatPermissions(_ context.Context, botID, chatID int64, rights domain.ChannelBannedRights) error {
	return f.record(chatAdminCall{method: "permissions", botID: botID, chatID: chatID, banned: rights})
}

func (f *fakeChatAdminGateway) BotAPIPinChatMessage(_ context.Context, botID, chatID int64, messageID int, silent bool) error {
	return f.record(chatAdminCall{method: "pin", botID: botID, chatID: chatID, messageID: messageID, flag: silent})
}

func (f *fakeChatAdminGateway) BotAPIUnpinChatMessage(_ context.Context, botID, chatID int64, messageID int) error {
	return f.record(chatAdminCall{method: "unpin", botID: botID, chatID: chatID, messageID: messageID})
}

func (f *fakeChatAdminGateway) BotAPIUnpinAllChatMessages(_ context.Context, botID, chatID int64) error {
	return f.record(chatAdminCall{method: "unpinAll", botID: botID, chatID: chatID})
}

func (f *fakeChatAdminGateway) BotAPISetChatTitle(_ context.Context, botID, chatID int64, title string) error {
	return f.record(chatAdminCall{method: "setTitle", botID: botID, chatID: chatID, text: title})
}

func (f *fakeChatAdminGateway) BotAPISetChatDescription(_ context.Context, botID, chatID int64, description string) error {
	return f.record(chatAdminCall{method: "setDescription", botID: botID, chatID: chatID, text: description})
}

func (f *fakeChatAdminGateway) BotAPISetChatPhoto(_ context.Context, botID, chatID int64, fileName, mimeType string, data []byte) error {
	return f.record(chatAdminCall{method: "setPhoto", botID: botID, chatID: chatID, text: fileName, data: data})
}

func (f *fakeChatAdminGateway) BotAPIDeleteChatPhoto(_ context.Context, botID, chatID int64) error {
	return f.record(chatAdminCall{method: "deletePhoto", botID: botID, chatID: chatID})
}

func (f *fakeChatAdminGateway) BotAPILeaveChat(_ context.Context, botID, chatID int64) error {
	return f.record(chatAdminCall{method: "leave", botID: botID, chatID: chatID})
}

// callChatAdmin 调用一个方法并断言返回 {ok:true, result:true}。
func callChatAdmin(t *testing.T, gateway GatewayService, method, body string) {
	t.Helper()
	rec := callMethod(t, newTestHandler(t, gateway), method, body)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK || string(resp.Result) != "true" {
		t.Fatalf("%s: status = %d body = %s, want ok true", method, rec.Code, rec.Body.String())
	}
}

// callChatAdminError 调用一个方法并返回失败描述。
func callChatAdminError(t *testing.T, gateway GatewayService, method, body string) string {
	t.Helper()
	rec := callMethod(t, newTestHandler(t, gateway), method, body)
	resp := decodeAPIResponse(t, rec)
	if resp.OK {
		t.Fatalf("%s: body = %s, want an error", method, rec.Body.String())
	}
	return resp.Description
}

func TestBanChatMemberPassesArgumentsAndReturnsTrue(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "banChatMember", `{"chat_id":-1001234,"user_id":42,"revoke_messages":true}`)
	call := gateway.lastCall(t)
	if call.method != "ban" || call.botID != testBotProfile.BotUserID || call.chatID != -1001234 || call.userID != 42 || !call.revoke || call.untilDate != 0 {
		t.Fatalf("call = %+v", call)
	}
}

func TestBanChatMemberNormalizesUntilDate(t *testing.T) {
	gateway := newFakeChatAdminGateway()
	future := time.Now().Unix() + 3600

	callChatAdmin(t, gateway, "banChatMember", fmt.Sprintf(`{"chat_id":-1001234,"user_id":42,"until_date":%d}`, future))
	if got := gateway.lastCall(t).untilDate; got != int(future) {
		t.Fatalf("until_date = %d, want %d kept", got, future)
	}
	// 不足 30 秒视为永久。
	callChatAdmin(t, gateway, "banChatMember", fmt.Sprintf(`{"chat_id":-1001234,"user_id":42,"until_date":%d}`, time.Now().Unix()+5))
	if got := gateway.lastCall(t).untilDate; got != 0 {
		t.Fatalf("until_date = %d, want 0 (forever)", got)
	}
}

func TestBanChatMemberRejectsBadArguments(t *testing.T) {
	gateway := newFakeChatAdminGateway()
	if got := callChatAdminError(t, gateway, "banChatMember", `{"chat_id":-1001234}`); got != "USER_ID_INVALID" {
		t.Fatalf("missing user_id: %q", got)
	}
	if got := callChatAdminError(t, gateway, "banChatMember", `{"chat_id":"oops","user_id":42}`); got != "CHAT_ID_INVALID" {
		t.Fatalf("bad chat_id: %q", got)
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("gateway called with bad arguments: %+v", gateway.calls)
	}
}

func TestUnbanChatMemberPassesOnlyIfBanned(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "unbanChatMember", `{"chat_id":"-1001234","user_id":"42","only_if_banned":true}`)
	if call := gateway.lastCall(t); call.method != "unban" || call.chatID != -1001234 || call.userID != 42 || !call.flag {
		t.Fatalf("call = %+v", call)
	}
	callChatAdmin(t, gateway, "unbanChatMember", `{"chat_id":-1001234,"user_id":42}`)
	if call := gateway.lastCall(t); call.flag {
		t.Fatalf("only_if_banned defaulted to true: %+v", call)
	}
}

func TestRestrictChatMemberConvertsPermissions(t *testing.T) {
	gateway := newFakeChatAdminGateway()
	future := time.Now().Unix() + 7200

	callChatAdmin(t, gateway, "restrictChatMember", fmt.Sprintf(
		`{"chat_id":-1001234,"user_id":42,"permissions":{"can_send_photos":true},"until_date":%d}`, future))
	call := gateway.lastCall(t)
	want := chatPermissionsToBanned(botAPIChatPermissions{CanSendPhotos: true}, false)
	want.UntilDate = int(future)
	if call.method != "restrict" || call.userID != 42 || call.banned != want {
		t.Fatalf("call = %+v, want rights %+v", call, want)
	}
	// 独立权限时不顺带放开纯文本。
	callChatAdmin(t, gateway, "restrictChatMember",
		`{"chat_id":-1001234,"user_id":42,"permissions":{"can_send_photos":true},"use_independent_chat_permissions":true}`)
	if got := gateway.lastCall(t).banned; !got.SendPlain || got.SendPhotos || got.UntilDate != 0 {
		t.Fatalf("independent rights = %+v", got)
	}
}

func TestRestrictChatMemberRequiresPermissions(t *testing.T) {
	gateway := newFakeChatAdminGateway()
	for _, body := range []string{
		`{"chat_id":-1001234,"user_id":42}`,
		`{"chat_id":-1001234,"user_id":42,"permissions":"not json"}`,
	} {
		if got := callChatAdminError(t, gateway, "restrictChatMember", body); got != "BAD_REQUEST" {
			t.Fatalf("%s: description = %q", body, got)
		}
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("gateway called: %+v", gateway.calls)
	}
}

func TestPromoteChatMemberReadsEveryRight(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "promoteChatMember",
		`{"chat_id":-1001234,"user_id":42,"is_anonymous":true,"can_delete_messages":true,"can_pin_messages":true,"can_manage_topics":false}`)
	call := gateway.lastCall(t)
	want := domain.ChannelAdminRights{Anonymous: true, DeleteMessages: true, PinMessages: true}
	if call.method != "promote" || call.userID != 42 || call.admin != want {
		t.Fatalf("call = %+v, want %+v", call, want)
	}
	// 全部缺省即撤销管理员。
	callChatAdmin(t, gateway, "promoteChatMember", `{"chat_id":-1001234,"user_id":42}`)
	if got := gateway.lastCall(t).admin; got != (domain.ChannelAdminRights{}) {
		t.Fatalf("rights = %+v, want none", got)
	}
}

func TestSetChatAdministratorCustomTitleChecksLength(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "setChatAdministratorCustomTitle", `{"chat_id":-1001234,"user_id":42,"custom_title":"十六个字的头衔十六个字的头衔十六"}`)
	if call := gateway.lastCall(t); call.method != "title" || call.userID != 42 || call.text != "十六个字的头衔十六个字的头衔十六" {
		t.Fatalf("call = %+v", call)
	}
	callChatAdmin(t, gateway, "setChatAdministratorCustomTitle", `{"chat_id":-1001234,"user_id":42}`)
	if call := gateway.lastCall(t); call.text != "" {
		t.Fatalf("empty title = %+v", call)
	}
	gateway.calls = nil
	if got := callChatAdminError(t, gateway, "setChatAdministratorCustomTitle", `{"chat_id":-1001234,"user_id":42,"custom_title":"seventeen chars!!"}`); got != "ADMIN_RANK_INVALID" {
		t.Fatalf("17 chars: description = %q", got)
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("gateway called: %+v", gateway.calls)
	}
}

func TestSetChatPermissionsConvertsPermissions(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "setChatPermissions",
		`{"chat_id":-1001234,"permissions":{"can_send_polls":true,"can_invite_users":true},"use_independent_chat_permissions":true}`)
	call := gateway.lastCall(t)
	want := chatPermissionsToBanned(botAPIChatPermissions{CanSendPolls: true, CanInviteUsers: true}, true)
	if call.method != "permissions" || call.chatID != -1001234 || call.banned != want {
		t.Fatalf("call = %+v, want %+v", call, want)
	}
	if got := callChatAdminError(t, gateway, "setChatPermissions", `{"chat_id":-1001234}`); got != "BAD_REQUEST" {
		t.Fatalf("missing permissions: %q", got)
	}
}

func TestPinChatMessageWorksForPrivateChatsAndGroups(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "pinChatMessage", `{"chat_id":2001,"message_id":7,"disable_notification":true}`)
	if call := gateway.lastCall(t); call.method != "pin" || call.chatID != 2001 || call.messageID != 7 || !call.flag {
		t.Fatalf("call = %+v", call)
	}
	callChatAdmin(t, gateway, "pinChatMessage", `{"chat_id":-1001234,"message_id":8}`)
	if call := gateway.lastCall(t); call.chatID != -1001234 || call.messageID != 8 || call.flag {
		t.Fatalf("call = %+v", call)
	}
	gateway.calls = nil
	if got := callChatAdminError(t, gateway, "pinChatMessage", `{"chat_id":2001}`); got != "MESSAGE_ID_INVALID" {
		t.Fatalf("missing message_id: %q", got)
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("gateway called: %+v", gateway.calls)
	}
}

func TestUnpinChatMessageMessageIDIsOptional(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "unpinChatMessage", `{"chat_id":-1001234,"message_id":9}`)
	if call := gateway.lastCall(t); call.method != "unpin" || call.messageID != 9 {
		t.Fatalf("call = %+v", call)
	}
	callChatAdmin(t, gateway, "unpinChatMessage", `{"chat_id":-1001234}`)
	if call := gateway.lastCall(t); call.method != "unpin" || call.messageID != 0 {
		t.Fatalf("call = %+v, want message_id 0 (most recent pin)", call)
	}
	if got := callChatAdminError(t, gateway, "unpinChatMessage", `{"chat_id":-1001234,"message_id":-3}`); got != "MESSAGE_ID_INVALID" {
		t.Fatalf("negative message_id: %q", got)
	}
}

func TestUnpinAllChatMessages(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "unpinAllChatMessages", `{"chat_id":2001}`)
	if call := gateway.lastCall(t); call.method != "unpinAll" || call.chatID != 2001 {
		t.Fatalf("call = %+v", call)
	}
}

func TestSetChatTitleChecksLength(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "setChatTitle", `{"chat_id":-1001234,"title":"New name"}`)
	if call := gateway.lastCall(t); call.method != "setTitle" || call.text != "New name" {
		t.Fatalf("call = %+v", call)
	}
	gateway.calls = nil
	if got := callChatAdminError(t, gateway, "setChatTitle", `{"chat_id":-1001234,"title":""}`); got != "CHAT_TITLE_EMPTY" {
		t.Fatalf("empty title: %q", got)
	}
	long := strings.Repeat("标", 129)
	if got := callChatAdminError(t, gateway, "setChatTitle", `{"chat_id":-1001234,"title":"`+long+`"}`); got != "TITLE_INVALID" {
		t.Fatalf("129-char title: %q", got)
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("gateway called: %+v", gateway.calls)
	}
}

func TestSetChatDescriptionChecksLength(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "setChatDescription", `{"chat_id":-1001234,"description":"about us"}`)
	if call := gateway.lastCall(t); call.method != "setDescription" || call.text != "about us" {
		t.Fatalf("call = %+v", call)
	}
	// 省略即清空。
	callChatAdmin(t, gateway, "setChatDescription", `{"chat_id":-1001234}`)
	if call := gateway.lastCall(t); call.text != "" {
		t.Fatalf("call = %+v", call)
	}
	gateway.calls = nil
	long := strings.Repeat("述", 256)
	if got := callChatAdminError(t, gateway, "setChatDescription", `{"chat_id":-1001234,"description":"`+long+`"}`); got != "CHAT_ABOUT_TOO_LONG" {
		t.Fatalf("256-char description: %q", got)
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("gateway called: %+v", gateway.calls)
	}
}

// postMultipart 以 multipart/form-data 调用方法；files 的键是表单字段名。
func postMultipart(t *testing.T, gateway GatewayService, method string, fields map[string]string, files map[string][]byte) apiResponse {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range fields {
		_ = writer.WriteField(k, v)
	}
	for field, data := range files {
		part, err := writer.CreateFormFile(field, field+".jpg")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		_, _ = part.Write(data)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	token := domain.FormatBotToken(testBotProfile.BotUserID, testBotProfile.TokenSecret)
	req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/"+method, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	newTestHandler(t, gateway).ServeHTTP(rec, req)
	return decodeAPIResponse(t, rec)
}

func TestSetChatPhotoTakesAnUploadedFile(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	resp := postMultipart(t, gateway, "setChatPhoto", map[string]string{"chat_id": "-1001234"}, map[string][]byte{"photo": []byte("jpeg bytes")})
	if !resp.OK || string(resp.Result) != "true" {
		t.Fatalf("response = %+v", resp)
	}
	if call := gateway.lastCall(t); call.method != "setPhoto" || call.chatID != -1001234 || string(call.data) != "jpeg bytes" || call.text != "photo.jpg" {
		t.Fatalf("call = %+v", call)
	}
	// attach:// 引用同一请求里的另一个文件字段。
	resp = postMultipart(t, gateway, "setChatPhoto", map[string]string{"chat_id": "-1001234", "photo": "attach://pic"}, map[string][]byte{"pic": []byte("other")})
	if !resp.OK || string(gateway.lastCall(t).data) != "other" {
		t.Fatalf("attach:// response = %+v call = %+v", resp, gateway.lastCall(t))
	}
}

func TestSetChatPhotoRejectsFileIDsAndURLs(t *testing.T) {
	gateway := newFakeChatAdminGateway()
	for _, photo := range []string{"", "AgACAgIAAxkBAAIB", "https://example.com/a.jpg"} {
		if got := callChatAdminError(t, gateway, "setChatPhoto", `{"chat_id":-1001234,"photo":"`+photo+`"}`); got != "PHOTO_INVALID" {
			t.Fatalf("photo %q: description = %q", photo, got)
		}
	}
	if len(gateway.calls) != 0 {
		t.Fatalf("gateway called: %+v", gateway.calls)
	}
}

func TestDeleteChatPhoto(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "deleteChatPhoto", `{"chat_id":-1001234}`)
	if call := gateway.lastCall(t); call.method != "deletePhoto" || call.chatID != -1001234 {
		t.Fatalf("call = %+v", call)
	}
}

func TestLeaveChat(t *testing.T) {
	gateway := newFakeChatAdminGateway()

	callChatAdmin(t, gateway, "leaveChat", `{"chat_id":-1001234}`)
	if call := gateway.lastCall(t); call.method != "leave" || call.chatID != -1001234 || call.botID != testBotProfile.BotUserID {
		t.Fatalf("call = %+v", call)
	}
}

func TestChatAdminMethodsNeedTheOptionalGateway(t *testing.T) {
	rec := callMethod(t, newTestHandler(t, &fakeBotAPIGateway{}), "banChatMember", `{"chat_id":-1001234,"user_id":42}`)
	if rec.Code != http.StatusNotImplemented || decodeAPIResponse(t, rec).Description != "METHOD_NOT_FOUND" {
		t.Fatalf("status = %d body = %s, want 501 METHOD_NOT_FOUND", rec.Code, rec.Body.String())
	}
}

func TestChatAdminErrorsKeepTheirTLCodes(t *testing.T) {
	for _, code := range chatAdminErrorMarkers {
		gateway := newFakeChatAdminGateway()
		gateway.err = errors.New("rpc error code 400: " + code)
		if got := callChatAdminError(t, gateway, "banChatMember", `{"chat_id":-1001234,"user_id":42}`); got != code {
			t.Fatalf("description = %q, want %q", got, code)
		}
	}
}
