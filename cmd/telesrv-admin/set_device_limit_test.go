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

// TestSetDeviceLimitAPIForwardsCommandAndRequiresAccountsManage 后台设置/清除设备上限：预览和
// 执行带着会话的 actor 转发给服务端 admin API；device_limit 为 null 时原样转发为清除；要求
// CSRF；没有 accounts.manage 的操作员被拒绝，请求到不了服务端。
func TestSetDeviceLimitAPIForwardsCommandAndRequiresAccountsManage(t *testing.T) {
	var mu sync.Mutex
	var calls []admin.SetDeviceLimitRequest
	var raw []map[string]json.RawMessage
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts/set-device-limit" || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		data, _ := json.Marshal(body)
		var req admin.SetDeviceLimitRequest
		if err := json.Unmarshal(data, &req); err != nil {
			t.Error(err)
		}
		mu.Lock()
		calls = append(calls, req)
		raw = append(raw, body)
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
		{"anonymous", false, false, `{"command_id":"exec-limit","reason":"staff","user_id":1001,"device_limit":2,"confirm":true}`, http.StatusUnauthorized},
		{"missing csrf", true, false, `{"command_id":"exec-limit","reason":"staff","user_id":1001,"device_limit":2,"confirm":true}`, http.StatusForbidden},
		{"preview", true, true, `{"command_id":"dry-limit","reason":"staff","user_id":1001,"device_limit":2,"confirm":false}`, http.StatusOK},
		{"execute", true, true, `{"command_id":"exec-limit","reason":"staff","user_id":1001,"device_limit":2,"confirm":true}`, http.StatusOK},
		{"clear", true, true, `{"command_id":"exec-limit-clear","reason":"staff","user_id":1001,"device_limit":null,"confirm":true}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/actions/set-device-limit", strings.NewReader(tc.body))
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
	if len(calls) != 3 {
		t.Fatalf("upstream calls=%d, want preview + execute + clear", len(calls))
	}
	for i, want := range []struct {
		id    string
		dry   bool
		limit *int
	}{{"dry-limit", true, intPtr(2)}, {"exec-limit", false, intPtr(2)}, {"exec-limit-clear", false, nil}} {
		call := calls[i]
		if call.CommandID != want.id || call.Actor == "" || call.Reason != "staff" || call.DryRun != want.dry || call.UserID != 1001 ||
			(call.DeviceLimit == nil) != (want.limit == nil) || (want.limit != nil && *call.DeviceLimit != *want.limit) {
			t.Fatalf("forwarded request[%d]=%+v, want %+v", i, call, want)
		}
	}
	if got := string(raw[2]["device_limit"]); got != "null" {
		t.Fatalf("clear forwarded device_limit=%q, want an explicit null", got)
	}
	mu.Unlock()

	forbidden := panelServer(t, permissionAccountsRead)
	forbidden.cfg.AdminAPIURL, forbidden.cfg.AdminAPIToken = upstream.URL, "test-admin"
	forbiddenCookies, forbiddenCSRF := signIn(t, forbidden)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-device-limit",
		strings.NewReader(`{"reason":"staff","user_id":1001,"device_limit":1,"confirm":true}`)), forbiddenCookies)
	req.Header.Set(csrfHeaderName, forbiddenCSRF)
	rec := httptest.NewRecorder()
	forbidden.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionAccountsManage) {
		t.Fatalf("without accounts.manage status=%d body=%s, want 403 naming the right", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("forbidden request reached the admin API: calls=%d", len(calls))
	}
}

func intPtr(v int) *int { return &v }

// TestDeviceBindingsAPIProxiesAndRequiresAccountsRead 账号的设备绑定名单（#113）：代理到服务端
// admin API 的 GET /v1/accounts/{id}/device-bindings，响应原样返回且不缓存；没有 accounts.read
// 的操作员被拒绝，请求到不了服务端。
func TestDeviceBindingsAPIProxiesAndRequiresAccountsRead(t *testing.T) {
	const body = `{"user_id":1001,"bindings":[{"device_id":"phone-1","device_model":"Pixel 9","platform":"Android","app_version":"11.2","bound_at":"2026-10-20T08:00:00Z","last_used_at":"2026-10-20T09:00:00Z"}]}`
	var mu sync.Mutex
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer upstream.Close()

	get := func(srv *server, cookies []*http.Cookie, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if cookies != nil {
			req = withCookies(req, cookies)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}

	srv := panelServer(t, permissionAccountsRead)
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = upstream.URL, "test-admin"
	cookies, _ := signIn(t, srv)
	if rec := get(srv, nil, "/api/accounts/1001/device-bindings"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d, want 401", rec.Code)
	}
	if rec := get(srv, cookies, "/api/accounts/abc/device-bindings"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	rec := get(srv, cookies, "/api/accounts/1001/device-bindings")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != body {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store", got)
	}
	mu.Lock()
	if len(paths) != 1 || paths[0] != "/v1/accounts/1001/device-bindings" {
		t.Fatalf("upstream paths=%v", paths)
	}
	mu.Unlock()

	forbidden := panelServer(t, permissionAccountsManage)
	forbidden.cfg.AdminAPIURL, forbidden.cfg.AdminAPIToken = upstream.URL, "test-admin"
	forbiddenCookies, _ := signIn(t, forbidden)
	rec = get(forbidden, forbiddenCookies, "/api/accounts/1001/device-bindings")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionAccountsRead) {
		t.Fatalf("without accounts.read status=%d body=%s, want 403 naming the right", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 {
		t.Fatalf("forbidden request reached the admin API: paths=%v", paths)
	}
}
