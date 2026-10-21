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

type captureSetDeviceLimitService struct {
	fakeService
	reqs []admin.SetDeviceLimitRequest
}

func (s *captureSetDeviceLimitService) SetDeviceLimit(_ context.Context, req admin.SetDeviceLimitRequest) (admin.CommandResult, error) {
	s.reqs = append(s.reqs, req)
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun, TargetUserID: req.UserID}, nil
}

func TestAdminAPISetDeviceLimit(t *testing.T) {
	svc := &captureSetDeviceLimitService{}
	srv := &Server{
		token:  "secret",
		scoped: []ScopedToken{{Name: "prices", Token: "scoped", Permissions: []string{PermissionPricesManage}}},
		svc:    svc,
		log:    zap.NewNop(),
	}
	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/accounts/set-device-limit", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	set := `{"command_id":"c-limit","actor":"ops","reason":"staff","user_id":1001,"device_limit":2}`
	if rec := post("", set); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a token status=%d, want 401", rec.Code)
	}
	if rec := post("scoped", set); rec.Code != http.StatusForbidden {
		t.Fatalf("with a scoped token status=%d, want 403", rec.Code)
	}
	if len(svc.reqs) != 0 {
		t.Fatalf("unauthorized requests reached the service %d times", len(svc.reqs))
	}

	if rec := post("secret", set); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"command_id":"c-limit"`) {
		t.Fatalf("set status=%d body=%s", rec.Code, rec.Body.String())
	}
	clear := `{"command_id":"c-limit-clear","actor":"ops","reason":"staff","dry_run":true,"user_id":1001,"device_limit":null}`
	if rec := post("secret", clear); rec.Code != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(svc.reqs) != 2 {
		t.Fatalf("service calls=%d, want 2", len(svc.reqs))
	}
	if got := svc.reqs[0]; got.CommandID != "c-limit" || got.Actor != "ops" || got.UserID != 1001 || got.DeviceLimit == nil || *got.DeviceLimit != 2 {
		t.Fatalf("decoded set request = %+v", got)
	}
	if got := svc.reqs[1]; got.CommandID != "c-limit-clear" || !got.DryRun || got.UserID != 1001 || got.DeviceLimit != nil {
		t.Fatalf("decoded clear request = %+v", got)
	}
}
