package adminapi

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestAdminAPICreateAccountForwardsPermissions 建号带权限（#162）：permissions 原样解码交给命令。
func TestAdminAPICreateAccountForwardsPermissions(t *testing.T) {
	svc := &captureCreateAccountService{}
	srv := &Server{token: "secret", svc: svc, log: zap.NewNop()}
	body := `{"command_id":"c-create-perm","actor":"ops","reason":"new hire","phone":"+15550007823","first_name":"Fay",` +
		`"permissions":{"enabled":true,"send":false,"send_whitelist":{"enabled":true,"user_ids":[7,8]}}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/accounts/create", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	p := svc.req.Permissions
	if p == nil || p.Enabled == nil || !*p.Enabled || p.Send == nil || *p.Send || p.Delete != nil ||
		p.SendWhitelist == nil || !*p.SendWhitelist.Enabled || !slices.Equal(*p.SendWhitelist.UserIDs, []int64{7, 8}) {
		t.Fatalf("decoded permissions = %+v", p)
	}
}
