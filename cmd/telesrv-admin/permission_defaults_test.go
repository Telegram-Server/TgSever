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

// TestSetPermissionDefaultsAPIForwardsCommandAndRequiresAccountsManage 设置默认权限模板（#161）：
// 预览和执行带着会话的 actor 转发给服务端 POST /v1/permission-defaults/set；要求 CSRF；没有
// accounts.manage 的操作员被拒绝。
func TestSetPermissionDefaultsAPIForwardsCommandAndRequiresAccountsManage(t *testing.T) {
	var mu sync.Mutex
	var calls []admin.SetPermissionDefaultsRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/permission-defaults/set" || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var req admin.SetPermissionDefaultsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		mu.Lock()
		calls = append(calls, req)
		mu.Unlock()
		writeJSON(w, http.StatusOK, admin.CommandResult{CommandID: req.CommandID, DryRun: req.DryRun, Status: "completed"})
	}))
	defer upstream.Close()

	const settings = `"enabled":true,"edit":false,"apply_to_self_signup":true,"send_whitelist":{"enabled":true,"user_ids":[7,8]}`
	post := func(srv *server, cookies []*http.Cookie, csrf, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/actions/set-permission-defaults", strings.NewReader(body))
		if cookies != nil {
			req = withCookies(req, cookies)
		}
		if csrf != "" {
			req.Header.Set(csrfHeaderName, csrf)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}

	srv := panelServer(t, permissionAccountsManage)
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = upstream.URL, "test-admin"
	cookies, csrf := signIn(t, srv)
	exec := `{"command_id":"exec-defaults","reason":"staff","confirm":true,` + settings + `}`
	if rec := post(srv, nil, "", exec); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", rec.Code)
	}
	if rec := post(srv, cookies, "", exec); rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", rec.Code)
	}
	if rec := post(srv, cookies, csrf, `{"command_id":"dry-defaults","reason":"staff","confirm":false,`+settings+`}`); rec.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := post(srv, cookies, csrf, exec); rec.Code != http.StatusOK {
		t.Fatalf("execute status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	if len(calls) != 2 {
		t.Fatalf("upstream calls=%d, want preview + execute", len(calls))
	}
	for i, want := range []struct {
		id  string
		dry bool
	}{{"dry-defaults", true}, {"exec-defaults", false}} {
		call := calls[i]
		if call.CommandID != want.id || call.Actor == "" || call.Reason != "staff" || call.DryRun != want.dry ||
			call.Enabled == nil || !*call.Enabled || call.Edit == nil || *call.Edit || call.Send != nil ||
			call.ApplyToSelfSignup == nil || !*call.ApplyToSelfSignup ||
			call.SendWhitelist == nil || call.SendWhitelist.UserIDs == nil || len(*call.SendWhitelist.UserIDs) != 2 {
			t.Fatalf("forwarded request[%d]=%+v", i, call)
		}
	}
	mu.Unlock()

	readOnly := panelServer(t, permissionAccountsRead)
	readOnly.cfg.AdminAPIURL, readOnly.cfg.AdminAPIToken = upstream.URL, "test-admin"
	roCookies, roCSRF := signIn(t, readOnly)
	if rec := post(readOnly, roCookies, roCSRF, exec); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionAccountsManage) {
		t.Fatalf("without accounts.manage status=%d body=%s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("forbidden request reached the admin API: calls=%d", len(calls))
	}
}

// TestPermissionDefaultsAPIProxiesAndRequiresAccountsRead 读取默认权限模板：代理到服务端
// GET /v1/permission-defaults，补上 send_whitelist.members 且不缓存；没有 accounts.read 的操作员被拒绝。
func TestPermissionDefaultsAPIProxiesAndRequiresAccountsRead(t *testing.T) {
	const body = `{"found":false,"enabled":false,"send":true,"apply_to_self_signup":false,"send_whitelist":{"enabled":true,"user_ids":[]}}`
	const want = `{"found":false,"enabled":false,"send":true,"apply_to_self_signup":false,"send_whitelist":{"enabled":true,"user_ids":[],"members":[]}}`
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
	get := func(srv *server, cookies []*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/permission-defaults", nil)
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
	if rec := get(srv, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d, want 401", rec.Code)
	}
	rec := get(srv, cookies)
	if rec.Code != http.StatusOK || !jsonEqual(t, rec.Body.String(), want) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("get status=%d body=%s cache=%q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}

	forbidden := panelServer(t, permissionAccountsManage)
	forbidden.cfg.AdminAPIURL, forbidden.cfg.AdminAPIToken = upstream.URL, "test-admin"
	forbiddenCookies, _ := signIn(t, forbidden)
	if rec := get(forbidden, forbiddenCookies); rec.Code != http.StatusForbidden {
		t.Fatalf("without accounts.read status=%d", rec.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/v1/permission-defaults" {
		t.Fatalf("upstream paths=%v", paths)
	}
}
