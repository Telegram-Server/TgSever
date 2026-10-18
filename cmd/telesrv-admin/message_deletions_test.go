package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The deletion log shows what users removed, including the content, so it is
// gated by its own right. messages.read must not open it: being able to browse
// live history is a working-level job, seeing what people deleted and from
// which device is not.
func TestMessageDeletionRoutesRequireTheirOwnRight(t *testing.T) {
	paths := []string{"/api/messages/deletions", "/api/messages/deletions/detail?id=1"}
	for _, path := range paths {
		rec := httptest.NewRecorder()
		panelServer(t, permissionAll).routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s without a session status=%d, want 401", path, rec.Code)
		}
	}

	srv := panelServer(t, permissionMessagesRead, permissionMessagesManage)
	cookies, _ := signIn(t, srv)
	for _, path := range paths {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, path, nil), cookies))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET %s with messages.read status=%d body=%s, want 403", path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode 403 body: %v", err)
		}
		if body["permission"] != permissionMessageDeletionsRead {
			t.Fatalf("GET %s 403 body=%+v, want %q named", path, body, permissionMessageDeletionsRead)
		}
	}
}

func TestMessageDeletionsReadIsAssignable(t *testing.T) {
	for _, name := range assignablePermissions() {
		if name == permissionMessageDeletionsRead {
			return
		}
	}
	t.Fatalf("%q missing from assignablePermissions: no named operator could ever be granted it", permissionMessageDeletionsRead)
}
