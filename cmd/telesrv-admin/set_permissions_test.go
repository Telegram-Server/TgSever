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

// TestSetPermissionsAPIForwardsCommandAndRequiresAccountsManage 后台设置员工权限（#119）：预览和
// 执行带着会话的 actor 转发给服务端 admin API；要求 CSRF；没有 accounts.manage 的操作员被拒绝。
func TestSetPermissionsAPIForwardsCommandAndRequiresAccountsManage(t *testing.T) {
	var mu sync.Mutex
	var calls []admin.SetPermissionsRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts/set-permissions" || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var req admin.SetPermissionsRequest
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
	const settings = `"user_id":1001,"enabled":true,"edit":false,"send_whitelist":{"enabled":true,"user_ids":[7,8]}`
	for _, tc := range []struct {
		name                string
		authenticated, csrf bool
		body                string
		status              int
	}{
		{"anonymous", false, false, `{"command_id":"exec-perm","reason":"staff","confirm":true,` + settings + `}`, http.StatusUnauthorized},
		{"missing csrf", true, false, `{"command_id":"exec-perm","reason":"staff","confirm":true,` + settings + `}`, http.StatusForbidden},
		{"preview", true, true, `{"command_id":"dry-perm","reason":"staff","confirm":false,` + settings + `}`, http.StatusOK},
		{"execute", true, true, `{"command_id":"exec-perm","reason":"staff","confirm":true,` + settings + `}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/actions/set-permissions", strings.NewReader(tc.body))
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
	}{{"dry-perm", true}, {"exec-perm", false}} {
		call := calls[i]
		if call.CommandID != want.id || call.Actor == "" || call.Reason != "staff" || call.DryRun != want.dry || call.UserID != 1001 ||
			call.Enabled == nil || !*call.Enabled || call.Edit == nil || *call.Edit || call.Send != nil ||
			call.SendWhitelist == nil || call.SendWhitelist.UserIDs == nil || len(*call.SendWhitelist.UserIDs) != 2 {
			t.Fatalf("forwarded request[%d]=%+v", i, call)
		}
	}
	mu.Unlock()

	forbidden := panelServer(t, permissionAccountsRead)
	forbidden.cfg.AdminAPIURL, forbidden.cfg.AdminAPIToken = upstream.URL, "test-admin"
	forbiddenCookies, forbiddenCSRF := signIn(t, forbidden)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-permissions",
		strings.NewReader(`{"reason":"staff","confirm":true,`+settings+`}`)), forbiddenCookies)
	req.Header.Set(csrfHeaderName, forbiddenCSRF)
	rec := httptest.NewRecorder()
	forbidden.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionAccountsManage) {
		t.Fatalf("without accounts.manage status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("forbidden request reached the admin API: calls=%d", len(calls))
	}
}

// TestAccountPermissionsAPIProxiesAndRequiresAccountsRead 账号的员工权限：代理到服务端
// GET /v1/accounts/{id}/permissions，原样返回且不缓存；没有 accounts.read 的操作员被拒绝。
func TestAccountPermissionsAPIProxiesAndRequiresAccountsRead(t *testing.T) {
	const body = `{"user_id":1001,"found":false,"enabled":false,"send":true}`
	var mu sync.Mutex
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if rec := get(srv, nil, "/api/accounts/1001/permissions"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d, want 401", rec.Code)
	}
	if rec := get(srv, cookies, "/api/accounts/abc/permissions"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id status=%d, want 400", rec.Code)
	}
	rec := get(srv, cookies, "/api/accounts/1001/permissions")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != body || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("get status=%d body=%s cache=%q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}

	forbidden := panelServer(t, permissionAccountsManage)
	forbidden.cfg.AdminAPIURL, forbidden.cfg.AdminAPIToken = upstream.URL, "test-admin"
	forbiddenCookies, _ := signIn(t, forbidden)
	if rec := get(forbidden, forbiddenCookies, "/api/accounts/1001/permissions"); rec.Code != http.StatusForbidden {
		t.Fatalf("without accounts.read status=%d", rec.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/v1/accounts/1001/permissions" {
		t.Fatalf("upstream paths=%v", paths)
	}
}
