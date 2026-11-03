package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 自动广播计划是单独一项权限：能发单次广播的操作员也不能建定时广播。
func TestBroadcastScheduleActionsRequireBroadcastsSchedule(t *testing.T) {
	srv := panelServer(t, permissionBroadcastsRead, permissionBroadcastsSend)
	cookies, token := signIn(t, srv)
	for _, path := range []string{
		"/api/actions/create-broadcast-schedule",
		"/api/actions/update-broadcast-schedule",
		"/api/actions/delete-broadcast-schedule",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set(csrfHeaderName, token)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(req, cookies))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s without broadcasts.schedule status=%d body=%s, want 403", path, rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode 403 body: %v", err)
		}
		if body["permission"] != permissionBroadcastsSchedule {
			t.Fatalf("%s 403 body=%+v, want %q named", path, body, permissionBroadcastsSchedule)
		}
	}
}

// 计划列表与广播列表同一项读权限：没有 broadcasts.read 看不到。
func TestBroadcastSchedulesListRequiresBroadcastsRead(t *testing.T) {
	srv := panelServer(t, permissionBroadcastsSchedule)
	cookies, _ := signIn(t, srv)
	req := httptest.NewRequest(http.MethodGet, "/api/broadcast-schedules", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(req, cookies))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("broadcast-schedules without broadcasts.read status=%d, want 403", rec.Code)
	}
}

func TestBroadcastsScheduleIsAssignable(t *testing.T) {
	for _, name := range assignablePermissions() {
		if name == permissionBroadcastsSchedule {
			return
		}
	}
	t.Fatalf("%q missing from assignablePermissions: no named operator could ever be granted it", permissionBroadcastsSchedule)
}
