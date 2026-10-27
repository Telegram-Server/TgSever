package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateAccountAPIForwardsPermissions 建号表单的权限区块（#162）：permissions 原样转发给服务端，
// 不给时请求里不带这个键（服务端据此套用默认权限模板）。
func TestCreateAccountAPIForwardsPermissions(t *testing.T) {
	var forwarded []map[string]json.RawMessage
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		forwarded = append(forwarded, body)
		writeJSON(w, http.StatusOK, map[string]any{"status": "completed"})
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
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	post(`{"command_id":"exec-create-perm-panel","reason":"new hire","confirm":true,"phone":"+15550007832","first_name":"Gus",` +
		`"permissions":{"enabled":true,"join_group":false,"send_whitelist":{"enabled":true,"user_ids":[9]}}}`)
	post(`{"command_id":"exec-create-noperm-panel","reason":"new hire","confirm":true,"phone":"+15550007833","first_name":"Gus"}`)

	if len(forwarded) != 2 {
		t.Fatalf("upstream calls = %d, want 2", len(forwarded))
	}
	const want = `{"enabled":true,"join_group":false,"send_whitelist":{"enabled":true,"user_ids":[9]}}`
	if got := string(forwarded[0]["permissions"]); got != want {
		t.Fatalf("forwarded permissions = %s, want %s", got, want)
	}
	if _, ok := forwarded[1]["permissions"]; ok {
		t.Fatalf("forwarded request without permissions = %v, want no permissions key", forwarded[1])
	}
}
