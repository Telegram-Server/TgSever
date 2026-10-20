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

// TestCreateAccountAPIForwardsCommandAndRequiresAccountsManage pins the panel
// side of admin account creation: preview and execute are forwarded to the
// server's admin API as one command (dry run, then confirm) with the session's
// actor, CSRF is enforced, and an operator without accounts.manage is refused.
func TestCreateAccountAPIForwardsCommandAndRequiresAccountsManage(t *testing.T) {
	var mu sync.Mutex
	var calls []admin.CreateAccountRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/accounts/create" || r.Header.Get("Authorization") != "Bearer test-admin" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}
		var body admin.CreateAccountRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		calls = append(calls, body)
		mu.Unlock()
		writeJSON(w, http.StatusOK, admin.CommandResult{CommandID: body.CommandID, DryRun: body.DryRun, Status: "completed"})
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
			body := `{"command_id":"dry-create-account-test","reason":"new hire","phone":"+15550007831","first_name":"Gus","last_name":"Ng","confirm":false}`
			if tc.confirm {
				body = strings.Replace(body, `"confirm":false`, `"confirm":true`, 1)
				body = strings.Replace(body, "dry-create-account-test", "exec-create-account-test", 1)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/actions/create-account", strings.NewReader(body))
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
	for i, call := range calls {
		wantID := "dry-create-account-test"
		if i == 1 {
			wantID = "exec-create-account-test"
		}
		if call.CommandID != wantID || call.Actor == "" || call.Reason != "new hire" || call.DryRun != (i == 0) ||
			call.Phone != "+15550007831" || call.FirstName != "Gus" || call.LastName != "Ng" {
			t.Fatalf("forwarded request[%d]=%+v", i, call)
		}
	}
	mu.Unlock()

	forbidden := panelServer(t, permissionAccountsRead)
	forbidden.cfg.AdminAPIURL, forbidden.cfg.AdminAPIToken = upstream.URL, "test-admin"
	forbiddenCookies, forbiddenCSRF := signIn(t, forbidden)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/create-account",
		strings.NewReader(`{"reason":"new hire","phone":"+15550007832","first_name":"Hal","confirm":true}`)), forbiddenCookies)
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
