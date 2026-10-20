package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/identity"
)

// 验证码登录开关与注册开关并列放在服务器设置页：后台报告实际状态和来源（环境变量
// TELESRV_CODE_LOGIN_ENABLED 或 identity.json 里的面板覆盖），以及密码登录总开关——
// 总开关关闭时服务端拒绝关闭验证码登录，页面据此提示。只有 server.manage 能读和改。

type codeLoginStateResponse struct {
	CodeLoginEnabled          *bool  `json:"code_login_enabled"`
	CodeLoginEnabledEffective bool   `json:"code_login_enabled_effective"`
	CodeLoginSource           string `json:"code_login_source"`
	DefaultCodeLoginEnabled   bool   `json:"default_code_login_enabled"`
	PasswordLoginEnabled      bool   `json:"password_login_enabled"`
	CodeLoginOverrideIgnored  bool   `json:"code_login_override_ignored"`
}

func getCodeLoginState(t *testing.T, srv *server, cookies []*http.Cookie) codeLoginStateResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/server/identity", nil), cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/server/identity status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out codeLoginStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	return out
}

func TestServerIdentityReportsCodeLoginStateAndSource(t *testing.T) {
	srv := panelServer(t, permissionServerManage)
	srv.cfg.CodeLoginEnabledDefault = true
	srv.cfg.PasswordLoginEnabled = true
	srv.identity = identity.NewStore(t.TempDir())
	cookies, _ := signIn(t, srv)

	got := getCodeLoginState(t, srv, cookies)
	if got.CodeLoginEnabled != nil || !got.CodeLoginEnabledEffective || got.CodeLoginSource != "env" || !got.DefaultCodeLoginEnabled || !got.PasswordLoginEnabled {
		t.Fatalf("no override: %+v, want effective=true source=env default=true password=true", got)
	}

	off := false
	if err := srv.identity.SetCodeLoginOverride(&off); err != nil {
		t.Fatal(err)
	}
	got = getCodeLoginState(t, srv, cookies)
	if got.CodeLoginEnabled == nil || *got.CodeLoginEnabled || got.CodeLoginEnabledEffective || got.CodeLoginSource != "override" {
		t.Fatalf("override off: %+v, want effective=false source=override", got)
	}

	if err := srv.identity.SetCodeLoginOverride(nil); err != nil {
		t.Fatal(err)
	}
	got = getCodeLoginState(t, srv, cookies)
	if got.CodeLoginEnabled != nil || !got.CodeLoginEnabledEffective || got.CodeLoginSource != "env" {
		t.Fatalf("cleared override: %+v, want back to env", got)
	}
}

// 不变量：密码登录总开关关闭时验证码登录一定开启。持久化的"关闭"覆盖值仍原样报告，
// 但实际状态显示为开启，并标明覆盖值被忽略。
func TestServerIdentityReportsCodeLoginOnWhilePasswordLoginDisabled(t *testing.T) {
	srv := panelServer(t, permissionServerManage)
	srv.cfg.CodeLoginEnabledDefault = true
	srv.cfg.PasswordLoginEnabled = false
	srv.identity = identity.NewStore(t.TempDir())
	cookies, _ := signIn(t, srv)

	if got := getCodeLoginState(t, srv, cookies); !got.CodeLoginEnabledEffective || got.CodeLoginOverrideIgnored {
		t.Fatalf("no override: %+v, want effective=true ignored=false", got)
	}
	off := false
	if err := srv.identity.SetCodeLoginOverride(&off); err != nil {
		t.Fatal(err)
	}
	got := getCodeLoginState(t, srv, cookies)
	if got.CodeLoginEnabled == nil || *got.CodeLoginEnabled || !got.CodeLoginEnabledEffective || !got.CodeLoginOverrideIgnored || got.CodeLoginSource != "override" {
		t.Fatalf("override off + password login off: %+v, want raw off, effective=true, ignored=true", got)
	}
}

func TestSetCodeLoginEnabledRequiresServerManage(t *testing.T) {
	anonymous := panelServer(t, permissionServerManage)
	rec := httptest.NewRecorder()
	anonymous.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/actions/set-code-login-enabled",
		strings.NewReader(`{"reason":"password only","confirm":true,"mode":"off"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", rec.Code, rec.Body.String())
	}

	srv, upstream, _ := forwardingPanel(t, permissionAccountsManage)
	cookies, csrf := signIn(t, srv)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-code-login-enabled",
		strings.NewReader(`{"reason":"password only","confirm":true,"mode":"off"}`)), cookies)
	req.Header.Set(csrfHeaderName, csrf)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionServerManage) {
		t.Fatalf("without server.manage status=%d body=%s", rec.Code, rec.Body.String())
	}
	if calls := upstream.snapshot(); len(calls) != 0 {
		t.Fatalf("forbidden request reached the server: %+v", calls)
	}
}
