package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 以账号身份建群会绕过该账号自己的建群权限，比改群设置强得多，所以单独一项权限：
// 只持有 channels.manage 的操作员不能用它。
func TestCreateGroupRequiresItsOwnRight(t *testing.T) {
	srv := panelServer(t, permissionChannelsRead, permissionChannelsManage)
	cookies, token := signIn(t, srv)
	req := httptest.NewRequest(http.MethodPost, "/api/actions/create-group", strings.NewReader(`{}`))
	req.Header.Set(csrfHeaderName, token)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(req, cookies))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("create-group with channels.manage status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 403 body: %v", err)
	}
	if body["permission"] != permissionChannelsCreate {
		t.Fatalf("create-group 403 body=%+v, want %q named", body, permissionChannelsCreate)
	}
}

func TestChannelsCreateIsAssignable(t *testing.T) {
	for _, name := range assignablePermissions() {
		if name == permissionChannelsCreate {
			return
		}
	}
	t.Fatalf("%q missing from assignablePermissions: no named operator could ever be granted it", permissionChannelsCreate)
}
