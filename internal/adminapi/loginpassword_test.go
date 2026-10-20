package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"telesrv/internal/admin"
)

type captureSetLoginPasswordService struct {
	fakeService
	req   admin.SetLoginPasswordRequest
	calls int
}

func (s *captureSetLoginPasswordService) SetLoginPassword(_ context.Context, req admin.SetLoginPasswordRequest) (admin.CommandResult, error) {
	s.req = req
	s.calls++
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun, TargetUserID: req.UserID,
		Details: map[string]any{"had_password": false, "revoke_sessions": req.RevokeSessions}}, nil
}

const setLoginPasswordMarker = "api-plaintext-marker-91c2"

func TestAdminAPISetLoginPassword(t *testing.T) {
	svc := &captureSetLoginPasswordService{}
	core, logs := observer.New(zapcore.DebugLevel)
	srv := &Server{
		token:  "secret",
		scoped: []ScopedToken{{Name: "prices", Token: "scoped", Permissions: []string{PermissionPricesManage}}},
		svc:    svc,
		log:    zap.New(core),
	}
	body := `{"command_id":"c-pw","actor":"ops","reason":"forgot password","dry_run":false,"user_id":1001,"password":"` + setLoginPasswordMarker + `","revoke_sessions":true}`
	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/accounts/set-login-password", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}

	if rec := post("", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a token status=%d, want 401", rec.Code)
	}
	// 受限 token 没有全部权限，不能借这条路设置密码。
	if rec := post("scoped", body); rec.Code != http.StatusForbidden {
		t.Fatalf("with a scoped token status=%d, want 403", rec.Code)
	}
	if svc.calls != 0 {
		t.Fatalf("unauthorized requests reached the service %d times", svc.calls)
	}

	rec := post("secret", body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"command_id":"c-pw"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := admin.SetLoginPasswordRequest{
		CommandMeta:    admin.CommandMeta{CommandID: "c-pw", Actor: "ops", Reason: "forgot password"},
		UserID:         1001,
		Password:       setLoginPasswordMarker,
		RevokeSessions: true,
	}
	if svc.req != want {
		t.Fatalf("decoded request = %+v, want %+v", svc.req, want)
	}
	if strings.Contains(rec.Body.String(), setLoginPasswordMarker) {
		t.Fatalf("response echoes the password: %s", rec.Body.String())
	}

	// 指纹只能由服务端算，调用方不能自己带。
	forged := strings.Replace(body, `"revoke_sessions":true`, `"password_fingerprint":"00"`, 1)
	if rec := post("secret", forged); rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), setLoginPasswordMarker) {
		t.Fatalf("caller-supplied fingerprint status=%d body=%s, want 400 without the password", rec.Code, rec.Body.String())
	}
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, setLoginPasswordMarker) || strings.Contains(fmt.Sprint(entry.ContextMap()), setLoginPasswordMarker) {
			t.Fatalf("a log entry carries the password: %+v", entry)
		}
	}
}
