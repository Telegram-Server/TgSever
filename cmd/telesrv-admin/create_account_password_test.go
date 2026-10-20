package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"telesrv/internal/admin"
)

// createAccountUpstreamBody 是后台转发给服务端 POST /v1/accounts/create 的请求体：可选的明文
// 登录密码只在这一跳出现，由服务端算 verifier。
type createAccountUpstreamBody struct {
	admin.CommandMeta
	Phone     string `json:"phone"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Password  string `json:"password,omitempty"`
}

// TestCreateAccountAPIForwardsTheLoginPassword 建号表单填了登录密码（#89）：预览和执行都把密码
// 带给服务端；不填时上游请求里没有 password 字段；面板的响应和日志里都不出现明文密码。
func TestCreateAccountAPIForwardsTheLoginPassword(t *testing.T) {
	const password = "panel-create-plaintext-marker-3e7b"
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	var mu sync.Mutex
	var raw []string
	var calls []createAccountUpstreamBody
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(r.Body); err != nil {
			t.Error(err)
		}
		dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
		dec.DisallowUnknownFields()
		var body createAccountUpstreamBody
		if err := dec.Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		raw = append(raw, buf.String())
		calls = append(calls, body)
		mu.Unlock()
		writeJSON(w, http.StatusOK, admin.CommandResult{CommandID: body.CommandID, DryRun: body.DryRun, Status: "completed",
			Details: map[string]any{"login_password": body.Password != ""}})
	}))
	defer upstream.Close()

	srv := panelServer(t, permissionAccountsManage)
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = upstream.URL, "test-admin"
	cookies, csrf := signIn(t, srv)
	post := func(body string) {
		t.Helper()
		req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/create-account", strings.NewReader(body)), cookies)
		req.Header.Set(csrfHeaderName, csrf)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), password) {
			t.Fatalf("status=%d body=%s, want 200 without the password", rec.Code, rec.Body.String())
		}
	}
	post(`{"command_id":"dry-create-pw","reason":"new hire","phone":"+15550007832","first_name":"Gus","password":"` + password + `","confirm":false}`)
	post(`{"command_id":"exec-create-pw","reason":"new hire","phone":"+15550007832","first_name":"Gus","password":"` + password + `","confirm":true}`)
	post(`{"command_id":"exec-create-no-pw","reason":"new hire","phone":"+15550007833","first_name":"Hal","confirm":true}`)

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("upstream calls=%d, want 3", len(calls))
	}
	for i, call := range calls[:2] {
		if call.Password != password || call.DryRun != (i == 0) || call.Phone != "+15550007832" {
			t.Fatalf("forwarded request[%d]=%+v, want the password forwarded", i, call)
		}
	}
	if strings.Contains(raw[2], "password") {
		t.Fatalf("a creation without a password forwarded %s", raw[2])
	}
	if strings.Contains(logs.String(), password) {
		t.Fatalf("panel logs carry the password: %s", logs.String())
	}
}
