package botapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// fakeDeleteMessageGateway 注入协议错误，验证 HTTP 删除错误的公开契约。
type fakeDeleteMessageGateway struct {
	*fakeBotAPIGateway
	err error
}

func (f *fakeDeleteMessageGateway) BotAPIDeleteMessage(_ context.Context, _ int64, _ int64, _ int) (bool, error) {
	return false, f.err
}

// deleteMessage 的失败说明与官方 Bot API 一致，bot 库按这两句文本判断删除失败的原因。
func TestDeleteMessageErrorDescriptions(t *testing.T) {
	for _, tc := range []struct {
		gatewayErr string
		want       string
	}{
		{"rpc error code 400: MESSAGE_ID_INVALID", "Bad Request: message to delete not found"},
		{"MESSAGE_ID_INVALID", "Bad Request: message to delete not found"},
		{"rpc error code 400: MESSAGE_DELETE_FORBIDDEN", "Bad Request: message can't be deleted"},
		{"CHAT_ID_INVALID", "CHAT_ID_INVALID"},
	} {
		t.Run(tc.gatewayErr, func(t *testing.T) {
			gateway := &fakeDeleteMessageGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}, err: errors.New(tc.gatewayErr)}

			rec := callMethod(t, newTestHandler(t, gateway), "deleteMessage", `{"chat_id":42,"message_id":7}`)
			resp := decodeAPIResponse(t, rec)
			if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != tc.want {
				t.Fatalf("status = %d body = %s, want 400 %q", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}
