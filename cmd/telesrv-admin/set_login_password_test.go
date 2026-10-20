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

// setLoginPasswordUpstreamBody 是后台转发给服务端 admin API 的请求体：明文密码只在这一跳出现，
// 由服务端算 verifier。
type setLoginPasswordUpstreamBody struct {
	admin.CommandMeta
	UserID         int64  `json:"user_id"`
	Password       string `json:"password"`
	RevokeSessions bool   `json:"revoke_sessions"`
}

// TestSetLoginPasswordAPIForwardsCommandAndRequiresAccountsManage 后台设置登录密码：预览和执行
// 作为同一个命令（先试运行、再确认）带着会话的 actor 转发给服务端 admin API；要求 CSRF；没有
// accounts.manage 的操作员被拒绝；面板的响应和日志里都不出现明文密码。
func TestSetLoginPasswordAPIForwardsCommandAndRequiresAccountsManage(t *testing.T) {
	const password = "panel-plaintext-marker-4d1e"
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	var mu sync.Mutex
	var calls []setLoginPasswordUpstreamBody
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts/set-login-password" || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var body setLoginPasswordUpstreamBody
		if err := dec.Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		calls = append(calls, body)
		mu.Unlock()
		writeJSON(w, http.StatusOK, admin.CommandResult{CommandID: body.CommandID, DryRun: body.DryRun, Status: "completed",
			TargetUserID: body.UserID, Details: map[string]any{"had_password": true, "revoke_sessions": body.RevokeSessions}})
	}))
	defer upstream.Close()

	srv := panelServer(t, permissionAccountsManage)
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = upstream.URL, "test-admin"
	cookies, csrf := signIn(t, srv)
	for _, tc := range []struct {
		name                         string
		authenticated, csrf, confirm bool
		status                       int
	}{
		{"anonymous", false, false, true, http.StatusUnauthorized},
		{"missing csrf", true, false, true, http.StatusForbidden},
		{"preview", true, true, false, http.StatusOK},
		{"execute", true, true, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"command_id":"dry-set-login-password-test","reason":"forgot password","user_id":1001,"password":"` + password + `","revoke_sessions":true,"confirm":false}`
			if tc.confirm {
				body = strings.Replace(body, `"confirm":false`, `"confirm":true`, 1)
				body = strings.Replace(body, "dry-set-login-password-test", "exec-set-login-password-test", 1)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/actions/set-login-password", strings.NewReader(body))
			if tc.authenticated {
				req = withCookies(req, cookies)
			}
			if tc.csrf {
				req.Header.Set(csrfHeaderName, csrf)
			}
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), password) {
				t.Fatalf("panel response echoes the password: %s", rec.Body.String())
			}
		})
	}
	mu.Lock()
	if len(calls) != 2 {
		t.Fatalf("upstream calls=%d, want preview + execute", len(calls))
	}
	for i, call := range calls {
		wantID := "dry-set-login-password-test"
		if i == 1 {
			wantID = "exec-set-login-password-test"
		}
		if call.CommandID != wantID || call.Actor == "" || call.Reason != "forgot password" || call.DryRun != (i == 0) ||
			call.UserID != 1001 || call.Password != password || !call.RevokeSessions {
			t.Fatalf("forwarded request[%d]=%+v", i, call)
		}
	}
	mu.Unlock()

	forbidden := panelServer(t, permissionAccountsRead)
	forbidden.cfg.AdminAPIURL, forbidden.cfg.AdminAPIToken = upstream.URL, "test-admin"
	forbiddenCookies, forbiddenCSRF := signIn(t, forbidden)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-login-password",
		strings.NewReader(`{"reason":"forgot","user_id":1001,"password":"`+password+`","confirm":true}`)), forbiddenCookies)
	req.Header.Set(csrfHeaderName, forbiddenCSRF)
	rec := httptest.NewRecorder()
	forbidden.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionAccountsManage) {
		t.Fatalf("without accounts.manage status=%d body=%s, want 403 naming the right", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if len(calls) != 2 {
		t.Fatalf("forbidden request reached the admin API: calls=%d", len(calls))
	}
	mu.Unlock()
	if strings.Contains(logs.String(), password) {
		t.Fatalf("panel logs carry the password: %s", logs.String())
	}
}
