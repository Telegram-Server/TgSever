package botapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// joinRequestCall 记录假网关收到的一次审批调用。
type joinRequestCall struct {
	approve bool
	botID   int64
	chatID  int64
	userID  int64
}

type fakeJoinRequestGateway struct {
	*fakeBotAPIGateway
	calls []joinRequestCall
	err   error
}

func newFakeJoinRequestGateway() *fakeJoinRequestGateway {
	return &fakeJoinRequestGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func (f *fakeJoinRequestGateway) BotAPIApproveChatJoinRequest(_ context.Context, botID, chatID, userID int64) error {
	f.calls = append(f.calls, joinRequestCall{approve: true, botID: botID, chatID: chatID, userID: userID})
	return f.err
}

func (f *fakeJoinRequestGateway) BotAPIDeclineChatJoinRequest(_ context.Context, botID, chatID, userID int64) error {
	f.calls = append(f.calls, joinRequestCall{approve: false, botID: botID, chatID: chatID, userID: userID})
	return f.err
}

func TestChatJoinRequestMethodsPassParameters(t *testing.T) {
	for _, tc := range []struct {
		method  string
		approve bool
	}{
		{method: "approveChatJoinRequest", approve: true},
		{method: "declineChatJoinRequest", approve: false},
	} {
		t.Run(tc.method, func(t *testing.T) {
			gateway := newFakeJoinRequestGateway()
			h := newTestHandler(t, gateway)

			rec := callMethod(t, h, tc.method, `{"chat_id":-1001234,"user_id":42}`)
			resp := decodeAPIResponse(t, rec)
			if rec.Code != http.StatusOK || !resp.OK || string(resp.Result) != "true" {
				t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
			}
			want := joinRequestCall{approve: tc.approve, botID: testBotProfile.BotUserID, chatID: -1001234, userID: 42}
			if len(gateway.calls) != 1 || gateway.calls[0] != want {
				t.Fatalf("calls = %+v, want %+v", gateway.calls, want)
			}
		})
	}
}

func TestChatJoinRequestMethodsRejectBadParameters(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing chat_id", body: `{"user_id":42}`, want: "CHAT_ID_INVALID"},
		{name: "missing user_id", body: `{"chat_id":-1001234}`, want: "USER_ID_INVALID"},
		{name: "non-positive user_id", body: `{"chat_id":-1001234,"user_id":0}`, want: "USER_ID_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := newFakeJoinRequestGateway()
			rec := callMethod(t, newTestHandler(t, gateway), "approveChatJoinRequest", tc.body)
			resp := decodeAPIResponse(t, rec)
			if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != tc.want {
				t.Fatalf("response = %d %s, want %s", rec.Code, rec.Body.String(), tc.want)
			}
			if len(gateway.calls) != 0 {
				t.Fatalf("gateway called with bad parameters: %+v", gateway.calls)
			}
		})
	}
}

// 处理器的 TL 错误码映射成官方 Bot API 的说明文本。
func TestChatJoinRequestErrorDescriptions(t *testing.T) {
	for _, tc := range []struct {
		err  string
		want string
	}{
		{err: "rpc error code 400: HIDE_REQUESTER_MISSING", want: "Bad Request: HIDE_REQUESTER_MISSING"},
		{err: "rpc error code 400: USER_ALREADY_PARTICIPANT", want: "Bad Request: USER_ALREADY_PARTICIPANT"},
		{err: "rpc error code 400: CHAT_ADMIN_REQUIRED", want: "Bad Request: CHAT_ADMIN_REQUIRED"},
		{err: "CHAT_ID_INVALID", want: "CHAT_ID_INVALID"},
	} {
		t.Run(tc.err, func(t *testing.T) {
			for _, method := range []string{"approveChatJoinRequest", "declineChatJoinRequest"} {
				gateway := newFakeJoinRequestGateway()
				gateway.err = errors.New(tc.err)
				rec := callMethod(t, newTestHandler(t, gateway), method, `{"chat_id":-1001234,"user_id":42}`)
				resp := decodeAPIResponse(t, rec)
				if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != tc.want {
					t.Fatalf("%s response = %d %s, want %q", method, rec.Code, rec.Body.String(), tc.want)
				}
			}
		})
	}
}

func TestChatJoinRequestMethodsNeedGateway(t *testing.T) {
	rec := callMethod(t, newTestHandler(t, &fakeBotAPIGateway{}), "approveChatJoinRequest", `{"chat_id":-1001234,"user_id":42}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("response = %d %s, want 501", rec.Code, rec.Body.String())
	}
}
