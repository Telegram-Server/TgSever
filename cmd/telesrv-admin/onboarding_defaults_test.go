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

// TestSetOnboardingDefaultsAPIForwardsCommandAndRequiresAccountsManage 设置新账号入驻模板：预览和
// 执行带着会话的 actor 转发给服务端 POST /v1/onboarding-defaults/set；要求 CSRF；没有
// accounts.manage 的操作员被拒绝。
func TestSetOnboardingDefaultsAPIForwardsCommandAndRequiresAccountsManage(t *testing.T) {
	var mu sync.Mutex
	var calls []admin.SetOnboardingDefaultsRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/onboarding-defaults/set" || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var req admin.SetOnboardingDefaultsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		mu.Lock()
		calls = append(calls, req)
		mu.Unlock()
		writeJSON(w, http.StatusOK, admin.CommandResult{CommandID: req.CommandID, DryRun: req.DryRun, Status: "completed"})
	}))
	defer upstream.Close()

	const settings = `"contacts":[{"user_id":7,"mutual":true},{"user_id":8,"mutual":false}],"channel_ids":[9]`
	post := func(srv *server, cookies []*http.Cookie, csrf, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/actions/set-onboarding-defaults", strings.NewReader(body))
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
	exec := `{"command_id":"exec-onboarding","reason":"staff","confirm":true,` + settings + `}`
	if rec := post(srv, nil, "", exec); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", rec.Code)
	}
	if rec := post(srv, cookies, "", exec); rec.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", rec.Code)
	}
	if rec := post(srv, cookies, csrf, `{"command_id":"dry-onboarding","reason":"staff","confirm":false,`+settings+`}`); rec.Code != http.StatusOK {
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
	}{{"dry-onboarding", true}, {"exec-onboarding", false}} {
		call := calls[i]
		if call.CommandID != want.id || call.Actor == "" || call.Reason != "staff" || call.DryRun != want.dry ||
			len(call.Contacts) != 2 || call.Contacts[0] != (admin.OnboardingContactSetting{UserID: 7, Mutual: true}) ||
			len(call.ChannelIDs) != 1 || call.ChannelIDs[0] != 9 {
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

// TestOnboardingDefaultsAPIProxiesAndRequiresAccountsRead 读取入驻模板：代理到服务端
// GET /v1/onboarding-defaults，补上 contact_members 与 channels（名单为空时是空数组）且不缓存；
// 没有 accounts.read 的操作员被拒绝。
func TestOnboardingDefaultsAPIProxiesAndRequiresAccountsRead(t *testing.T) {
	const body = `{"found":false,"contacts":[],"channel_ids":[]}`
	const want = `{"found":false,"contacts":[],"channel_ids":[],"contact_members":[],"channels":[]}`
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
		req := httptest.NewRequest(http.MethodGet, "/api/onboarding-defaults", nil)
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
	if len(paths) != 1 || paths[0] != "/v1/onboarding-defaults" {
		t.Fatalf("upstream paths=%v", paths)
	}
}
