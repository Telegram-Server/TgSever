package botapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// fakeDeleteMessagesGateway 记录 deleteMessages 收到的 chat_id 与消息 id，并按 err 返回网关错误。
type fakeDeleteMessagesGateway struct {
	*fakeBotAPIGateway
	calls  int
	chatID int64
	ids    []int
	err    error
}

func (f *fakeDeleteMessagesGateway) BotAPIDeleteMessages(_ context.Context, _ int64, chatID int64, messageIDs []int) error {
	f.calls++
	f.chatID, f.ids = chatID, append([]int(nil), messageIDs...)
	return f.err
}

func newFakeDeleteMessagesGateway() *fakeDeleteMessagesGateway {
	return &fakeDeleteMessagesGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

func TestDeleteMessagesPassesChatAndDistinctIDs(t *testing.T) {
	gateway := newFakeDeleteMessagesGateway()

	rec := callMethod(t, newTestHandler(t, gateway), "deleteMessages", `{"chat_id":"-1001234","message_ids":[9,3,9,5]}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK || string(resp.Result) != "true" {
		t.Fatalf("status = %d body = %s, want ok true", rec.Code, rec.Body.String())
	}
	if gateway.chatID != -1001234 || !slices.Equal(gateway.ids, []int{3, 5, 9}) {
		t.Fatalf("gateway got chat %d ids %v, want -1001234 [3 5 9]", gateway.chatID, gateway.ids)
	}
}

// message_ids 为空、超过 100 条或含非法 id 时在网关层直接返回参数错误，不触达网关。
func TestDeleteMessagesRejectsInvalidMessageIDs(t *testing.T) {
	hundredAndOne := make([]string, 101)
	for i := range hundredAndOne {
		hundredAndOne[i] = fmt.Sprint(i + 1)
	}
	for name, tc := range map[string]struct {
		body string
		want string
	}{
		"missing":      {`{"chat_id":42}`, "Bad Request: message identifiers are not specified"},
		"empty":        {`{"chat_id":42,"message_ids":[]}`, "Bad Request: message identifiers are not specified"},
		"too many":     {`{"chat_id":42,"message_ids":[` + strings.Join(hundredAndOne, ",") + `]}`, "Bad Request: too many message identifiers specified"},
		"zero":         {`{"chat_id":42,"message_ids":[1,0]}`, "Bad Request: invalid message identifier specified"},
		"negative":     {`{"chat_id":42,"message_ids":[-3]}`, "Bad Request: invalid message identifier specified"},
		"not a number": {`{"chat_id":42,"message_ids":["x"]}`, "Bad Request: invalid message identifier specified"},
		"not an array": {`{"chat_id":42,"message_ids":7}`, "Bad Request: invalid message identifier specified"},
	} {
		t.Run(name, func(t *testing.T) {
			gateway := newFakeDeleteMessagesGateway()

			rec := callMethod(t, newTestHandler(t, gateway), "deleteMessages", tc.body)
			resp := decodeAPIResponse(t, rec)
			if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != tc.want {
				t.Fatalf("status = %d body = %s, want 400 %q", rec.Code, rec.Body.String(), tc.want)
			}
			if gateway.calls != 0 {
				t.Fatalf("gateway called %d times for invalid message_ids", gateway.calls)
			}
		})
	}
}

func TestDeleteMessagesAcceptsAHundredIDs(t *testing.T) {
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprint(i + 1)
	}
	gateway := newFakeDeleteMessagesGateway()

	rec := callMethod(t, newTestHandler(t, gateway), "deleteMessages", `{"chat_id":42,"message_ids":[`+strings.Join(ids, ",")+`]}`)
	if resp := decodeAPIResponse(t, rec); rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("status = %d body = %s, want ok", rec.Code, rec.Body.String())
	}
	if len(gateway.ids) != 100 {
		t.Fatalf("gateway got %d ids, want 100", len(gateway.ids))
	}
}

// 全部删不了时，网关错误与 deleteMessage 一样映射成官方说明。
func TestDeleteMessagesErrorDescriptions(t *testing.T) {
	for _, tc := range []struct {
		gatewayErr string
		want       string
	}{
		{"rpc error code 400: MESSAGE_ID_INVALID", "Bad Request: message to delete not found"},
		{"rpc error code 400: MESSAGE_DELETE_FORBIDDEN", "Bad Request: message can't be deleted"},
		{"CHAT_ID_INVALID", "CHAT_ID_INVALID"},
	} {
		t.Run(tc.gatewayErr, func(t *testing.T) {
			gateway := newFakeDeleteMessagesGateway()
			gateway.err = errors.New(tc.gatewayErr)

			rec := callMethod(t, newTestHandler(t, gateway), "deleteMessages", `{"chat_id":42,"message_ids":[7,8]}`)
			resp := decodeAPIResponse(t, rec)
			if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != tc.want {
				t.Fatalf("status = %d body = %s, want 400 %q", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

func TestDeleteMessagesWithoutGatewaySupportIsNotFound(t *testing.T) {
	rec := callMethod(t, newTestHandler(t, &fakeBotAPIGateway{}), "deleteMessages", `{"chat_id":42,"message_ids":[7]}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d body = %s, want 501", rec.Code, rec.Body.String())
	}
}
