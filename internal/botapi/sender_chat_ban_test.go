package botapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// senderChatBanCall 记录假网关收到的一次频道身份封禁/解封调用。
type senderChatBanCall struct {
	ban          bool
	botID        int64
	chatID       int64
	senderChatID int64
}

type fakeSenderChatBanGateway struct {
	*fakeBotAPIGateway
	calls []senderChatBanCall
	err   error
}

func newFakeSenderChatBanGateway() *fakeSenderChatBanGateway {
	return &fakeSenderChatBanGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func (f *fakeSenderChatBanGateway) BotAPIBanChatSenderChat(_ context.Context, botID, chatID, senderChatID int64) error {
	f.calls = append(f.calls, senderChatBanCall{ban: true, botID: botID, chatID: chatID, senderChatID: senderChatID})
	return f.err
}

func (f *fakeSenderChatBanGateway) BotAPIUnbanChatSenderChat(_ context.Context, botID, chatID, senderChatID int64) error {
	f.calls = append(f.calls, senderChatBanCall{ban: false, botID: botID, chatID: chatID, senderChatID: senderChatID})
	return f.err
}

func TestSenderChatBanMethodsPassParameters(t *testing.T) {
	for _, tc := range []struct {
		method string
		ban    bool
	}{
		{method: "banChatSenderChat", ban: true},
		{method: "unbanChatSenderChat", ban: false},
	} {
		t.Run(tc.method, func(t *testing.T) {
			gateway := newFakeSenderChatBanGateway()
			rec := callMethod(t, newTestHandler(t, gateway), tc.method, `{"chat_id":-1001234,"sender_chat_id":"-1000000005678"}`)
			resp := decodeAPIResponse(t, rec)
			if rec.Code != http.StatusOK || !resp.OK || string(resp.Result) != "true" {
				t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
			}
			want := senderChatBanCall{ban: tc.ban, botID: testBotProfile.BotUserID, chatID: -1001234, senderChatID: -1000000005678}
			if len(gateway.calls) != 1 || gateway.calls[0] != want {
				t.Fatalf("calls = %+v, want %+v", gateway.calls, want)
			}
		})
	}
}

// sender_chat_id 缺失、无法解析或不是超级群/频道的 chat_id 时是参数错误，不调用网关。
func TestSenderChatBanMethodsRejectBadSenderChatID(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{name: "missing sender_chat_id", body: `{"chat_id":-1001234}`, want: "Bad Request: invalid chat identifier specified"},
		{name: "not a number", body: `{"chat_id":-1001234,"sender_chat_id":"@spam"}`, want: "Bad Request: invalid chat identifier specified"},
		{name: "user id", body: `{"chat_id":-1001234,"sender_chat_id":42}`, want: "Bad Request: invalid chat identifier specified"},
		{name: "basic group id", body: `{"chat_id":-1001234,"sender_chat_id":-42}`, want: "Bad Request: invalid chat identifier specified"},
		{name: "missing chat_id", body: `{"sender_chat_id":-1000000005678}`, want: "CHAT_ID_INVALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, method := range []string{"banChatSenderChat", "unbanChatSenderChat"} {
				gateway := newFakeSenderChatBanGateway()
				rec := callMethod(t, newTestHandler(t, gateway), method, tc.body)
				resp := decodeAPIResponse(t, rec)
				if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != tc.want {
					t.Fatalf("%s response = %d %s, want %q", method, rec.Code, rec.Body.String(), tc.want)
				}
				if len(gateway.calls) != 0 {
					t.Fatalf("%s gateway called with bad parameters: %+v", method, gateway.calls)
				}
			}
		})
	}
}

// 处理器的 TL 错误码映射成官方 Bot API 的说明文本。
func TestSenderChatBanErrorDescriptions(t *testing.T) {
	for _, tc := range []struct {
		err  string
		want string
	}{
		{err: "rpc error code 400: CHAT_ADMIN_REQUIRED", want: "Bad Request: not enough rights to restrict/unrestrict chat member"},
		{err: "rpc error code 400: PEER_ID_INVALID", want: "Bad Request: member not found"},
		{err: "CHAT_ID_INVALID", want: "CHAT_ID_INVALID"},
	} {
		t.Run(tc.err, func(t *testing.T) {
			for _, method := range []string{"banChatSenderChat", "unbanChatSenderChat"} {
				gateway := newFakeSenderChatBanGateway()
				gateway.err = errors.New(tc.err)
				rec := callMethod(t, newTestHandler(t, gateway), method, `{"chat_id":-1001234,"sender_chat_id":-1000000005678}`)
				resp := decodeAPIResponse(t, rec)
				if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != tc.want {
					t.Fatalf("%s response = %d %s, want %q", method, rec.Code, rec.Body.String(), tc.want)
				}
			}
		})
	}
}

func TestSenderChatBanMethodsNeedGateway(t *testing.T) {
	for _, method := range []string{"banChatSenderChat", "unbanChatSenderChat"} {
		rec := callMethod(t, newTestHandler(t, &fakeBotAPIGateway{}), method, `{"chat_id":-1001234,"sender_chat_id":-1000000005678}`)
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s response = %d %s, want 501", method, rec.Code, rec.Body.String())
		}
	}
}
