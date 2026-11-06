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

type captureOnboardingDefaultsService struct {
	fakeService
	sets  []admin.SetOnboardingDefaultsRequest
	reads int
}

func (s *captureOnboardingDefaultsService) SetOnboardingDefaults(_ context.Context, req admin.SetOnboardingDefaultsRequest) (admin.CommandResult, error) {
	s.sets = append(s.sets, req)
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed", DryRun: req.DryRun}, nil
}

func (s *captureOnboardingDefaultsService) OnboardingDefaults(context.Context) (admin.OnboardingDefaultsResult, error) {
	s.reads++
	return admin.OnboardingDefaultsResult{Found: true, Contacts: []admin.OnboardingContactSetting{{UserID: 7, Mutual: true}}, ChannelIDs: []int64{}}, nil
}

// TestAdminAPIOnboardingDefaults 读写新账号入驻模板：与默认权限模板一样只有 master token 能调。
func TestAdminAPIOnboardingDefaults(t *testing.T) {
	svc := &captureOnboardingDefaultsService{}
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
	set := `{"command_id":"c-onboarding","actor":"ops","reason":"staff","contacts":[{"user_id":7,"mutual":true}],"channel_ids":[9]}`
	for _, token := range []string{"", "scoped"} {
		want := http.StatusUnauthorized
		if token != "" {
			want = http.StatusForbidden
		}
		if rec := do(http.MethodPost, "/v1/onboarding-defaults/set", token, set); rec.Code != want {
			t.Fatalf("set with token %q status=%d, want %d", token, rec.Code, want)
		}
		if rec := do(http.MethodGet, "/v1/onboarding-defaults", token, ""); rec.Code != want {
			t.Fatalf("get with token %q status=%d, want %d", token, rec.Code, want)
		}
	}
	if len(svc.sets)+svc.reads != 0 {
		t.Fatal("unauthorized requests reached the service")
	}

	if rec := do(http.MethodPost, "/v1/onboarding-defaults/set", "secret", set); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"command_id":"c-onboarding"`) {
		t.Fatalf("set status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := svc.sets[0]
	if len(got.Contacts) != 1 || got.Contacts[0] != (admin.OnboardingContactSetting{UserID: 7, Mutual: true}) || len(got.ChannelIDs) != 1 || got.ChannelIDs[0] != 9 {
		t.Fatalf("decoded set request = %+v", got)
	}
	rec := do(http.MethodGet, "/v1/onboarding-defaults", "secret", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"contacts":[{"user_id":7,"mutual":true}]`) || !strings.Contains(rec.Body.String(), `"channel_ids":[]`) {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}
}
