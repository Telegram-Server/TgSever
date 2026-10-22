package botapi

import (
	"net/http"
	"reflect"
	"testing"
)

// logOut / close 只为迁移兼容：固定返回 True，不触碰网关，之后的调用照常工作。
func TestLifecycleMethodsReturnTrueAndKeepBotWorking(t *testing.T) {
	for _, method := range []string{"logOut", "close"} {
		t.Run(method, func(t *testing.T) {
			gateway := &fakeBotAPIGateway{}
			h := newTestHandler(t, gateway)

			rec := callMethod(t, h, method, `{}`)
			resp := decodeAPIResponse(t, rec)
			if rec.Code != http.StatusOK || !resp.OK || string(resp.Result) != "true" {
				t.Fatalf("%s: status = %d body = %s, want 200 ok result=true", method, rec.Code, rec.Body.String())
			}
			if !reflect.DeepEqual(gateway, &fakeBotAPIGateway{}) {
				t.Fatalf("%s touched the gateway: %#v", method, gateway)
			}

			// 再调一次也不报错（官方 close 的 10 分钟 429 限制不照搬）。
			if rec := callMethod(t, h, method, ``); rec.Code != http.StatusOK || !decodeAPIResponse(t, rec).OK {
				t.Fatalf("%s repeated: status = %d body = %s", method, rec.Code, rec.Body.String())
			}

			rec = callMethod(t, h, "sendMessage", `{"chat_id":2001,"text":"hello"}`)
			if rec.Code != http.StatusOK || !decodeAPIResponse(t, rec).OK {
				t.Fatalf("sendMessage after %s: status = %d body = %s", method, rec.Code, rec.Body.String())
			}
			if !gateway.sendCalled || gateway.sendChatID != 2001 || gateway.sendText != "hello" {
				t.Fatalf("sendMessage after %s did not reach the gateway: %#v", method, gateway)
			}
		})
	}
}
