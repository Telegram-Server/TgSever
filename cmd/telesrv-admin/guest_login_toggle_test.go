package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"telesrv/internal/identity"
)

// 游客登录开关与注册开关、验证码登录开关放在同一页：后台报告开关与 IP 绑定天数的实际值和来源
// （环境变量或 identity.json 里的面板覆盖）。写入经 admin API 转发，见 serversettings_forward_test.go。

type guestLoginStateResponse struct {
	GuestLoginEnabled          *bool  `json:"guest_login_enabled"`
	GuestIPBindDays            *int   `json:"guest_ip_bind_days"`
	DefaultGuestLoginEnabled   bool   `json:"default_guest_login_enabled"`
	GuestLoginEnabledEffective bool   `json:"guest_login_enabled_effective"`
	GuestLoginSource           string `json:"guest_login_source"`
	DefaultGuestIPBindDays     int    `json:"default_guest_ip_bind_days"`
	GuestIPBindDaysEffective   int    `json:"guest_ip_bind_days_effective"`
	GuestIPBindDaysSource      string `json:"guest_ip_bind_days_source"`
}

func getGuestLoginState(t *testing.T, srv *server, cookies []*http.Cookie) guestLoginStateResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/server/identity", nil), cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/server/identity status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out guestLoginStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	return out
}

func TestServerIdentityReportsGuestLoginStateAndSource(t *testing.T) {
	srv := panelServer(t, permissionServerManage)
	srv.cfg.GuestLoginEnabledDefault = false
	srv.cfg.GuestIPBindDaysDefault = 7
	srv.identity = identity.NewStore(t.TempDir())
	cookies, _ := signIn(t, srv)

	got := getGuestLoginState(t, srv, cookies)
	if got.GuestLoginEnabled != nil || got.GuestIPBindDays != nil || got.GuestLoginEnabledEffective || got.GuestLoginSource != "env" ||
		got.GuestIPBindDaysEffective != 7 || got.GuestIPBindDaysSource != "env" || got.DefaultGuestIPBindDays != 7 {
		t.Fatalf("no override: %+v, want off / 7 days from env", got)
	}

	on, days := true, 30
	if err := srv.identity.SetGuestLoginOverride(&on, &days); err != nil {
		t.Fatal(err)
	}
	got = getGuestLoginState(t, srv, cookies)
	if got.GuestLoginEnabled == nil || !*got.GuestLoginEnabled || !got.GuestLoginEnabledEffective || got.GuestLoginSource != "override" ||
		got.GuestIPBindDaysEffective != 30 || got.GuestIPBindDaysSource != "override" {
		t.Fatalf("override on / 30: %+v", got)
	}

	if err := srv.identity.SetGuestLoginOverride(nil, nil); err != nil {
		t.Fatal(err)
	}
	if got = getGuestLoginState(t, srv, cookies); got.GuestLoginEnabledEffective || got.GuestLoginSource != "env" || got.GuestIPBindDaysEffective != 7 {
		t.Fatalf("cleared overrides: %+v, want back to env", got)
	}
}
