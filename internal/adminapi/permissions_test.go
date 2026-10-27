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

type capturePermissionsService struct {
	fakeService
	sets  []admin.SetPermissionsRequest
	reads []int64
}

func (s *capturePermissionsService) SetPermissions(_ context.Context, req admin.SetPermissionsRequest) (admin.CommandResult, error) {
	s.sets = append(s.sets, req)
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun, TargetUserID: req.UserID}, nil
}

func (s *capturePermissionsService) AccountPermissions(_ context.Context, userID int64) (admin.AccountPermissionsResult, error) {
	s.reads = append(s.reads, userID)
	return admin.AccountPermissionsResult{UserID: userID, Send: true, SendWhitelist: admin.PermissionsWhitelist{UserIDs: []int64{}}}, nil
}

// TestAdminAPIPermissions 读写账号的员工权限（#119）：与设备上限一样只有 master token 能调。
func TestAdminAPIPermissions(t *testing.T) {
	svc := &capturePermissionsService{}
	srv := &Server{
		token:  "secret",
		scoped: []ScopedToken{{Name: "prices", Token: "scoped", Permissions: []string{PermissionPricesManage}}},
		svc:    svc,
		log:    zap.NewNop(),
	}
	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	set := `{"command_id":"c-perm","actor":"ops","reason":"staff","user_id":1001,"enabled":true,"edit":false,"send_whitelist":{"enabled":true,"user_ids":[7,8]}}`
	for _, token := range []string{"", "scoped"} {
		want := http.StatusUnauthorized
		if token != "" {
			want = http.StatusForbidden
		}
		if rec := do(http.MethodPost, "/v1/accounts/set-permissions", token, set); rec.Code != want {
			t.Fatalf("set with token %q status=%d, want %d", token, rec.Code, want)
		}
		if rec := do(http.MethodGet, "/v1/accounts/1001/permissions", token, ""); rec.Code != want {
			t.Fatalf("get with token %q status=%d, want %d", token, rec.Code, want)
		}
	}
	if len(svc.sets)+len(svc.reads) != 0 {
		t.Fatal("unauthorized requests reached the service")
	}

	if rec := do(http.MethodPost, "/v1/accounts/set-permissions", "secret", set); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"command_id":"c-perm"`) {
		t.Fatalf("set status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := svc.sets[0]
	if got.UserID != 1001 || got.Enabled == nil || !*got.Enabled || got.Edit == nil || *got.Edit || got.Send != nil ||
		got.SendWhitelist == nil || got.SendWhitelist.UserIDs == nil || len(*got.SendWhitelist.UserIDs) != 2 {
		t.Fatalf("decoded set request = %+v", got)
	}

	rec := do(http.MethodGet, "/v1/accounts/1001/permissions", "secret", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"user_id":1001`) || !strings.Contains(rec.Body.String(), `"user_ids":[]`) {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodGet, "/v1/accounts/nope/permissions", "secret", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id status=%d, want 400", rec.Code)
	}
}

type capturePermissionDefaultsService struct {
	fakeService
	sets  []admin.SetPermissionDefaultsRequest
	reads int
}

func (s *capturePermissionDefaultsService) SetPermissionDefaults(_ context.Context, req admin.SetPermissionDefaultsRequest) (admin.CommandResult, error) {
	s.sets = append(s.sets, req)
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (s *capturePermissionDefaultsService) PermissionDefaults(context.Context) (admin.PermissionDefaultsResult, error) {
	s.reads++
	return admin.PermissionDefaultsResult{Found: true, Send: true, ApplyToSelfSignup: true, SendWhitelist: admin.PermissionsWhitelist{UserIDs: []int64{}}}, nil
}

// TestAdminAPIPermissionDefaults 读写默认权限模板（#161）：与账号权限一样只有 master token 能调。
func TestAdminAPIPermissionDefaults(t *testing.T) {
	svc := &capturePermissionDefaultsService{}
	srv := &Server{
		token:  "secret",
		scoped: []ScopedToken{{Name: "prices", Token: "scoped", Permissions: []string{PermissionPricesManage}}},
		svc:    svc,
		log:    zap.NewNop(),
	}
	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	set := `{"command_id":"c-defaults","actor":"ops","reason":"staff","enabled":true,"edit":false,"apply_to_self_signup":true,"send_whitelist":{"enabled":true,"user_ids":[7]}}`
	for _, token := range []string{"", "scoped"} {
		want := http.StatusUnauthorized
		if token != "" {
			want = http.StatusForbidden
		}
		if rec := do(http.MethodPost, "/v1/permission-defaults/set", token, set); rec.Code != want {
			t.Fatalf("set with token %q status=%d, want %d", token, rec.Code, want)
		}
		if rec := do(http.MethodGet, "/v1/permission-defaults", token, ""); rec.Code != want {
			t.Fatalf("get with token %q status=%d, want %d", token, rec.Code, want)
		}
	}
	if len(svc.sets)+svc.reads != 0 {
		t.Fatal("unauthorized requests reached the service")
	}

	if rec := do(http.MethodPost, "/v1/permission-defaults/set", "secret", set); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"command_id":"c-defaults"`) {
		t.Fatalf("set status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := svc.sets[0]
	if got.Enabled == nil || !*got.Enabled || got.Edit == nil || *got.Edit || got.Send != nil || got.ApplyToSelfSignup == nil || !*got.ApplyToSelfSignup ||
		got.SendWhitelist == nil || got.SendWhitelist.UserIDs == nil || len(*got.SendWhitelist.UserIDs) != 1 {
		t.Fatalf("decoded set request = %+v", got)
	}
	rec := do(http.MethodGet, "/v1/permission-defaults", "secret", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"apply_to_self_signup":true`) || !strings.Contains(rec.Body.String(), `"user_ids":[]`) {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}
}
