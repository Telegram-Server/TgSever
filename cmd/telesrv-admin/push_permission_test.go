package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 推送开关是单独一项权限：管账号、管群的操作员都不能用它。
func TestSetPushDeviceEnabledRequiresPushManage(t *testing.T) {
	srv := panelServer(t, permissionAccountsRead, permissionAccountsManage, permissionChannelsRead, permissionChannelsManage)
	cookies, token := signIn(t, srv)
	req := httptest.NewRequest(http.MethodPost, "/api/actions/set-push-device-enabled", strings.NewReader(`{}`))
	req.Header.Set(csrfHeaderName, token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(req, cookies))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("set-push-device-enabled without push.manage status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 403 body: %v", err)
	}
	if body["permission"] != permissionPushManage {
		t.Fatalf("set-push-device-enabled 403 body=%+v, want %q named", body, permissionPushManage)
	}
}

func TestPushManageIsAssignable(t *testing.T) {
	for _, name := range assignablePermissions() {
		if name == permissionPushManage {
			return
		}
	}
	t.Fatalf("%q missing from assignablePermissions: no named operator could ever be granted it", permissionPushManage)
}
