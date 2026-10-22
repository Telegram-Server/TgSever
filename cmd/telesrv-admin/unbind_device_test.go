package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"telesrv/internal/admin"
)

// TestUnbindDeviceAPIForwardsCommandAndRequiresAccountsManage 后台解绑设备（#114）：预览和执行
// 带着会话的 actor 转发给服务端 admin API 的 POST /v1/accounts/unbind-device；要求 CSRF；
// 没有 accounts.manage 的操作员（只能查看绑定名单）被拒绝，请求到不了服务端。
func TestUnbindDeviceAPIForwardsCommandAndRequiresAccountsManage(t *testing.T) {
	var mu sync.Mutex
	var calls []admin.UnbindDeviceRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts/unbind-device" || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var req admin.UnbindDeviceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		mu.Lock()
		calls = append(calls, req)
		mu.Unlock()
		writeJSON(w, http.StatusOK, admin.CommandResult{CommandID: req.CommandID, DryRun: req.DryRun, Status: "completed", TargetUserID: req.UserID})
	}))
	defer upstream.Close()

	srv := panelServer(t, permissionAccountsManage)
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = upstream.URL, "test-admin"
	cookies, csrf := signIn(t, srv)
	for _, tc := range []struct {
		name                string
		authenticated, csrf bool
		body                string
		status              int
	}{
		{"anonymous", false, false, `{"command_id":"exec-unbind","reason":"new laptop","user_id":1001,"device_id":"laptop-1","confirm":true}`, http.StatusUnauthorized},
		{"missing csrf", true, false, `{"command_id":"exec-unbind","reason":"new laptop","user_id":1001,"device_id":"laptop-1","confirm":true}`, http.StatusForbidden},
		{"preview", true, true, `{"command_id":"dry-unbind","reason":"new laptop","user_id":1001,"device_id":"laptop-1","confirm":false}`, http.StatusOK},
		{"execute", true, true, `{"command_id":"exec-unbind","reason":"new laptop","user_id":1001,"device_id":"laptop-1","confirm":true}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/actions/unbind-device", strings.NewReader(tc.body))
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
		})
	}
	mu.Lock()
	if len(calls) != 2 {
		t.Fatalf("upstream calls=%d, want preview + execute", len(calls))
	}
	for i, want := range []struct {
		id  string
		dry bool
	}{{"dry-unbind", true}, {"exec-unbind", false}} {
		call := calls[i]
		if call.CommandID != want.id || call.Actor == "" || call.Reason != "new laptop" || call.DryRun != want.dry || call.UserID != 1001 || call.DeviceID != "laptop-1" {
			t.Fatalf("forwarded request[%d]=%+v, want %+v", i, call, want)
		}
	}
	mu.Unlock()

	forbidden := panelServer(t, permissionAccountsRead)
	forbidden.cfg.AdminAPIURL, forbidden.cfg.AdminAPIToken = upstream.URL, "test-admin"
	forbiddenCookies, forbiddenCSRF := signIn(t, forbidden)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/unbind-device",
		strings.NewReader(`{"reason":"new laptop","user_id":1001,"device_id":"laptop-1","confirm":true}`)), forbiddenCookies)
	req.Header.Set(csrfHeaderName, forbiddenCSRF)
	rec := httptest.NewRecorder()
	forbidden.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionAccountsManage) {
		t.Fatalf("without accounts.manage status=%d body=%s, want 403 naming the right", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("forbidden request reached the admin API: calls=%d", len(calls))
	}
}
