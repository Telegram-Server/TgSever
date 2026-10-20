package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/adminapi"
	"telesrv/internal/identity"
	"telesrv/internal/store/postgres"
)

// 防锁死走完整链路：服务端密码登录总开关关闭时，后台发来的"关闭验证码登录"被服务端
// 拒绝，后台把稳定错误码 CODE_LOGIN_LOCKOUT 原样交给前端，identity 不变、不留命令。
func TestSetCodeLoginEnabledRefusedWhilePasswordLoginIsOff(t *testing.T) {
	srv, store := operatorServer(t)
	serverDir := t.TempDir()
	svc := admin.NewService(admin.Dependencies{
		Commands:                postgres.NewAdminStore(store.pool),
		ServerIdentity:          identity.NewStore(serverDir),
		CodeLoginEnabledDefault: func() bool { return true },
		PasswordLoginEnabled:    func() bool { return false },
	})
	api := httptest.NewServer(adminapi.NewHandler(adminapi.Config{Token: "chain-token"}, svc, nil))
	t.Cleanup(api.Close)
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = api.URL, "chain-token"
	srv.identity = identity.NewStore(serverDir)
	cookies, csrf := signIn(t, srv)

	id := fmt.Sprintf("code-login-lockout-%d", time.Now().UnixNano())
	t.Cleanup(func() { deleteCommandRows(store, id) })
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-code-login-enabled",
		strings.NewReader(fmt.Sprintf(`{"command_id":%q,"reason":"password only","confirm":true,"mode":"off"}`, id))), cookies)
	req.Header.Set(csrfHeaderName, csrf)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), admin.CodeCodeLoginLockout) {
		t.Fatalf("status=%d body=%s, want a %s refusal", rec.Code, rec.Body.String(), admin.CodeCodeLoginLockout)
	}
	if info, err := srv.identity.Get(); err != nil || info.CodeLoginEnabled != nil {
		t.Fatalf("refused switch wrote the override: %+v err=%v", info, err)
	}
	if commands, audits := commandRowCounts(t, store, id); commands != 0 || audits != 0 {
		t.Fatalf("refused switch left commands=%d audits=%d, want none", commands, audits)
	}
}
