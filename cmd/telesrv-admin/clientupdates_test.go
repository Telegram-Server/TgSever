package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientUpdatesPanelProxyAndPermission(t *testing.T) {
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.Header.Get("Authorization") != "Bearer updates-secret" {
			t.Fatalf("upstream authorization = %q", r.Header.Get("Authorization"))
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/client-updates" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"mode":"in_process","public_url":"https://chat.example/_updates","entries":[{"platform":"linux","channel":"stable","build":6000000}]}`))
	}))
	defer upstream.Close()

	srv := panelServer(t, permissionUpdatesRead)
	srv.cfg.AdminAPIURL = upstream.URL
	srv.cfg.AdminAPIToken = "updates-secret"
	cookies, _ := signIn(t, srv)

	read := withCookies(httptest.NewRequest(http.MethodGet, "/api/client-updates", nil), cookies)
	readResponse := httptest.NewRecorder()
	srv.routes().ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK || upstreamCalls != 1 ||
		!strings.Contains(readResponse.Body.String(), `"mode":"in_process"`) ||
		readResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("read status=%d calls=%d cache=%q body=%s", readResponse.Code, upstreamCalls,
			readResponse.Header().Get("Cache-Control"), readResponse.Body.String())
	}

	// 只有发布权限的会话也看不到状态：查看权限要单独授予。
	forbidden := panelServer(t, permissionUpdatesPublish)
	forbidden.cfg.AdminAPIURL = upstream.URL
	forbiddenCookies, _ := signIn(t, forbidden)
	forbiddenResponse := httptest.NewRecorder()
	forbidden.routes().ServeHTTP(forbiddenResponse,
		withCookies(httptest.NewRequest(http.MethodGet, "/api/client-updates", nil), forbiddenCookies))
	if forbiddenResponse.Code != http.StatusForbidden || upstreamCalls != 1 ||
		!strings.Contains(forbiddenResponse.Body.String(), permissionUpdatesRead) {
		t.Fatalf("forbidden status=%d calls=%d body=%s", forbiddenResponse.Code, upstreamCalls, forbiddenResponse.Body.String())
	}
}

func TestClientUpdatePermissionsAreAssignable(t *testing.T) {
	assignable := make(map[string]bool)
	for _, name := range assignablePermissions() {
		assignable[name] = true
	}
	for _, want := range []string{"updates.read", "updates.publish"} {
		if !assignable[want] {
			t.Errorf("%q missing from assignablePermissions: no named operator could ever be granted it", want)
		}
	}
	if err := validatePermissions([]string{permissionUpdatesRead, permissionUpdatesPublish}); err != nil {
		t.Fatalf("validatePermissions(updates rights) = %v", err)
	}
}
