package botapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"telesrv/internal/domain"
)

// testBotProfile 是 newTestHandler 认证用的机器人；callMethod 自动带上它的 token。
var testBotProfile = domain.BotProfile{BotUserID: 1001, TokenSecret: "secret"}

// newTestHandler 用给定网关搭一个只认 testBotProfile 的 handler。各组的测试在自己
// 的文件里定义内嵌 *fakeBotAPIGateway 的假网关，补上本组的可选接口后传进来，
// 不需要改 server_test.go。
func newTestHandler(t *testing.T, gateway GatewayService) http.Handler {
	t.Helper()
	bots := &fakeBotAPIBots{profile: testBotProfile}
	return (&handler{bots: bots, gateway: gateway}).routes()
}

// callMethod 以 JSON body 调用一个 Bot API 方法。
func callMethod(t *testing.T, h http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	return performBotAPIRequest(t, h, testBotProfile, method, body)
}

// decodeAPIResponse 解出统一的 {ok, result, description} 外壳。
func decodeAPIResponse(t *testing.T, rec *httptest.ResponseRecorder) apiResponse {
	t.Helper()
	var resp apiResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return resp
}
