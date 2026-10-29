package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"telesrv/internal/admin"
)

type captureSetPushDeviceEnabledService struct {
	fakeService
	reqs []admin.SetPushDeviceEnabledRequest
}

func (s *captureSetPushDeviceEnabledService) SetPushDeviceEnabled(_ context.Context, req admin.SetPushDeviceEnabledRequest) (admin.CommandResult, error) {
	s.reqs = append(s.reqs, req)
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun, TargetUserID: req.UserID}, nil
}

// 接线：POST /v1/push/set-device-enabled 只认完整 token，auth_key_id 按十进制字符串解码。
func TestAdminAPISetPushDeviceEnabled(t *testing.T) {
	svc := &captureSetPushDeviceEnabledService{}
	srv := &Server{
		token:  "secret",
		scoped: []ScopedToken{{Name: "prices", Token: "scoped", Permissions: []string{PermissionPricesManage}}},
		svc:    svc,
		log:    zap.NewNop(),
	}
	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/push/set-device-enabled", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	body := `{"command_id":"c-push","actor":"ops","reason":"release build","user_id":1001,"auth_key_id":"-9223372036854775807","enabled":true}`
	if rec := post("scoped", body); rec.Code != http.StatusForbidden {
		t.Fatalf("with a scoped token status=%d, want 403", rec.Code)
	}
	if rec := post("secret", body); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"command_id":"c-push"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(svc.reqs) != 1 {
		t.Fatalf("service calls=%d, want 1", len(svc.reqs))
	}
	if got := svc.reqs[0]; got.CommandID != "c-push" || got.UserID != 1001 || got.AuthKeyID != -9223372036854775807 || !got.Enabled {
		t.Fatalf("decoded request = %+v", got)
	}
}
