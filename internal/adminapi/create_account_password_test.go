package adminapi

import (
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

const createAccountPasswordMarker = "api-create-plaintext-marker-6a0f"

// TestAdminAPICreateAccountWithPassword 建号带登录密码（#89）：明文密码从请求体解码后交给命令，
// 响应和日志里都不出现；指纹只能由服务端算，调用方带 password_fingerprint 按未知字段拒绝。
func TestAdminAPICreateAccountWithPassword(t *testing.T) {
	svc := &captureCreateAccountService{}
	core, logs := observer.New(zapcore.DebugLevel)
	srv := &Server{token: "secret", svc: svc, log: zap.New(core)}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/accounts/create", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	body := `{"command_id":"c-create-pw","actor":"ops","reason":"new hire","dry_run":false,"phone":"+15550007822","first_name":"Fay","password":"` + createAccountPasswordMarker + `"}`

	rec := post(body)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), createAccountPasswordMarker) {
		t.Fatalf("status=%d body=%s, want 200 without the password", rec.Code, rec.Body.String())
	}
	want := admin.CreateAccountRequest{
		CommandMeta: admin.CommandMeta{CommandID: "c-create-pw", Actor: "ops", Reason: "new hire"},
		Phone:       "+15550007822", FirstName: "Fay", Password: createAccountPasswordMarker,
	}
	if svc.req != want {
		t.Fatalf("decoded request = %+v, want %+v", svc.req, want)
	}

	forged := strings.Replace(body, `"first_name":"Fay"`, `"first_name":"Fay","password_fingerprint":"00"`, 1)
	if rec := post(forged); rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), createAccountPasswordMarker) {
		t.Fatalf("caller-supplied fingerprint status=%d body=%s, want 400 without the password", rec.Code, rec.Body.String())
	}
	for _, entry := range logs.All() {
		if strings.Contains(entry.Message, createAccountPasswordMarker) || strings.Contains(fmt.Sprint(entry.ContextMap()), createAccountPasswordMarker) {
			t.Fatalf("a log entry carries the password: %+v", entry)
		}
	}
}
