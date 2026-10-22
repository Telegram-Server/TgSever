package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

type deviceBindingsService struct {
	fakeService
	calls []int64
}

func (s *deviceBindingsService) DeviceBindings(_ context.Context, userID int64) (admin.DeviceBindingList, error) {
	s.calls = append(s.calls, userID)
	if userID != 1001 {
		return admin.DeviceBindingList{UserID: userID, Bindings: []admin.DeviceBinding{}}, nil
	}
	at := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	return admin.DeviceBindingList{UserID: userID, Bindings: []admin.DeviceBinding{{
		DeviceID: "phone-1", DeviceModel: "Pixel 9", Platform: "Android", AppVersion: "11.2", BoundAt: at, LastUsedAt: at.Add(time.Hour),
	}}}, nil
}

// TestAdminAPIDeviceBindings 读账号的设备绑定名单（#113）：与 set-device-limit 一样只有 master
// token 能调；没有绑定的账号返回空数组。
func TestAdminAPIDeviceBindings(t *testing.T) {
	svc := &deviceBindingsService{}
	srv := &Server{
		token:  "secret",
		scoped: []ScopedToken{{Name: "prices", Token: "scoped", Permissions: []string{PermissionPricesManage}}},
		svc:    svc,
		log:    zap.NewNop(),
	}
	get := func(token, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	if rec := get("", "/v1/accounts/1001/device-bindings"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without a token status=%d, want 401", rec.Code)
	}
	if rec := get("scoped", "/v1/accounts/1001/device-bindings"); rec.Code != http.StatusForbidden {
		t.Fatalf("with a scoped token status=%d, want 403", rec.Code)
	}
	if rec := get("secret", "/v1/accounts/nope/device-bindings"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id status=%d, want 400", rec.Code)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("rejected requests reached the service %d times", len(svc.calls))
	}

	rec := get("secret", "/v1/accounts/1001/device-bindings")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := `{"user_id":1001,"bindings":[{"device_id":"phone-1","device_model":"Pixel 9","platform":"Android","app_version":"11.2",` +
		`"bound_at":"2026-10-20T08:00:00Z","last_used_at":"2026-10-20T09:00:00Z"}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("list body=%s\nwant %s", got, want)
	}
	rec = get("secret", "/v1/accounts/1002/device-bindings")
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != `{"user_id":1002,"bindings":[]}` {
		t.Fatalf("empty list status=%d body=%s", rec.Code, got)
	}
}
