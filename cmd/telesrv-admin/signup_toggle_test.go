package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/identity"
)

// The sign-up switch lives on Server Settings: the panel reports the effective
// state together with where it comes from (the TELESRV_SIGN_UP_ENABLED env
// default or the panel override in identity.json), and only server.manage may
// read or change it.

type signUpStateResponse struct {
	SignUpEnabled          *bool  `json:"sign_up_enabled"`
	SignUpEnabledEffective bool   `json:"sign_up_enabled_effective"`
	SignUpSource           string `json:"sign_up_source"`
	DefaultSignUpEnabled   bool   `json:"default_sign_up_enabled"`
}

func getSignUpState(t *testing.T, srv *server, cookies []*http.Cookie) signUpStateResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/server/identity", nil), cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/server/identity status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out signUpStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	return out
}

func TestServerIdentityReportsSignUpStateAndSource(t *testing.T) {
	srv := panelServer(t, permissionServerManage)
	srv.cfg.SignUpEnabledDefault = false
	srv.identity = identity.NewStore(t.TempDir())
	cookies, _ := signIn(t, srv)

	got := getSignUpState(t, srv, cookies)
	if got.SignUpEnabled != nil || got.SignUpEnabledEffective || got.SignUpSource != "env" || got.DefaultSignUpEnabled {
		t.Fatalf("no override: %+v, want effective=false source=env default=false", got)
	}

	on := true
	if err := srv.identity.SetSignUpOverride(&on); err != nil {
		t.Fatal(err)
	}
	got = getSignUpState(t, srv, cookies)
	if got.SignUpEnabled == nil || !*got.SignUpEnabled || !got.SignUpEnabledEffective || got.SignUpSource != "override" {
		t.Fatalf("override on: %+v, want effective=true source=override", got)
	}

	if err := srv.identity.SetSignUpOverride(nil); err != nil {
		t.Fatal(err)
	}
	got = getSignUpState(t, srv, cookies)
	if got.SignUpEnabled != nil || got.SignUpEnabledEffective || got.SignUpSource != "env" {
		t.Fatalf("cleared override: %+v, want back to env", got)
	}
}

func TestSetSignUpEnabledRequiresServerManage(t *testing.T) {
	anonymous := panelServer(t, permissionServerManage)
	rec := httptest.NewRecorder()
	anonymous.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/actions/set-sign-up-enabled",
		strings.NewReader(`{"reason":"abuse","confirm":true,"mode":"off"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", rec.Code, rec.Body.String())
	}

	srv := panelServer(t, permissionAccountsManage)
	srv.identity = identity.NewStore(t.TempDir())
	cookies, csrf := signIn(t, srv)

	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-sign-up-enabled",
		strings.NewReader(`{"reason":"abuse","confirm":true,"mode":"off"}`)), cookies)
	req.Header.Set(csrfHeaderName, csrf)
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionServerManage) {
		t.Fatalf("without server.manage status=%d body=%s", rec.Code, rec.Body.String())
	}
	if info, err := srv.identity.Get(); err != nil || info.SignUpEnabled != nil {
		t.Fatalf("forbidden request changed the override: %+v err=%v", info, err)
	}

	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/server/identity", nil), cookies))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("GET identity without server.manage status=%d", rec.Code)
	}
}

func TestSetSignUpEnabledRejectsUnknownMode(t *testing.T) {
	srv := panelServer(t, permissionServerManage)
	srv.identity = identity.NewStore(t.TempDir())
	cookies, csrf := signIn(t, srv)
	for _, body := range []string{
		`{"reason":"r","confirm":true}`,
		`{"reason":"r","confirm":true,"mode":"maybe"}`,
	} {
		req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-sign-up-enabled", strings.NewReader(body)), cookies)
		req.Header.Set(csrfHeaderName, csrf)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("body %s: status=%d resp=%s, want 422", body, rec.Code, rec.Body.String())
		}
	}
}
