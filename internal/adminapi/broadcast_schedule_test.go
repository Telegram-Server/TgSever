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

type captureBroadcastScheduleService struct {
	fakeService
	created []admin.CreateBroadcastScheduleRequest
	updated []admin.UpdateBroadcastScheduleRequest
	deleted []admin.DeleteBroadcastScheduleRequest
}

func (s *captureBroadcastScheduleService) CreateBroadcastSchedule(_ context.Context, req admin.CreateBroadcastScheduleRequest) (admin.CommandResult, error) {
	s.created = append(s.created, req)
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed"}, nil
}

func (s *captureBroadcastScheduleService) UpdateBroadcastSchedule(_ context.Context, req admin.UpdateBroadcastScheduleRequest) (admin.CommandResult, error) {
	s.updated = append(s.updated, req)
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed"}, nil
}

func (s *captureBroadcastScheduleService) DeleteBroadcastSchedule(_ context.Context, req admin.DeleteBroadcastScheduleRequest) (admin.CommandResult, error) {
	s.deleted = append(s.deleted, req)
	return admin.CommandResult{CommandID: req.CommandID, Status: "completed"}, nil
}

// 接线：三条计划命令只认完整 token；schedule_id 按十进制字符串解码，计划字段平铺在请求里。
func TestAdminAPIBroadcastScheduleCommands(t *testing.T) {
	svc := &captureBroadcastScheduleService{}
	srv := &Server{
		token:  "secret",
		scoped: []ScopedToken{{Name: "prices", Token: "scoped", Permissions: []string{PermissionPricesManage}}},
		svc:    svc,
		log:    zap.NewNop(),
	}
	post := func(token, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	fields := `"message":"hi","timezone":"Asia/Shanghai","time_of_day":840,"weekdays":[1,3,5],"enabled":true`
	cases := []struct{ path, body string }{
		{"/v1/broadcasts/schedules/create", `{"command_id":"c-create","actor":"ops","reason":"r",` + fields + `}`},
		{"/v1/broadcasts/schedules/update", `{"command_id":"c-update","actor":"ops","reason":"r","schedule_id":"42",` + fields + `}`},
		{"/v1/broadcasts/schedules/delete", `{"command_id":"c-delete","actor":"ops","reason":"r","schedule_id":"42"}`},
	}
	for _, c := range cases {
		if rec := post("scoped", c.path, c.body); rec.Code != http.StatusForbidden {
			t.Fatalf("%s with a scoped token status=%d, want 403", c.path, rec.Code)
		}
		if rec := post("secret", c.path, c.body); rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", c.path, rec.Code, rec.Body.String())
		}
	}
	if len(svc.created) != 1 || len(svc.updated) != 1 || len(svc.deleted) != 1 {
		t.Fatalf("calls create/update/delete = %d/%d/%d", len(svc.created), len(svc.updated), len(svc.deleted))
	}
	if got := svc.created[0]; got.Message != "hi" || got.TimeOfDay != 840 || len(got.Weekdays) != 3 || !got.Enabled || got.Timezone != "Asia/Shanghai" {
		t.Fatalf("decoded create = %+v", got)
	}
	if got := svc.updated[0]; got.ScheduleID != 42 || got.CommandID != "c-update" || got.Message != "hi" {
		t.Fatalf("decoded update = %+v", got)
	}
	if got := svc.deleted[0]; got.ScheduleID != 42 {
		t.Fatalf("decoded delete = %+v", got)
	}
}
