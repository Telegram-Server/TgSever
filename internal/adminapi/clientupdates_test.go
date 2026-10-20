package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/admin"
)

type clientUpdatesCaptureService struct {
	fakeService
	statusRead bool
}

func (s *clientUpdatesCaptureService) ClientUpdateStatus(context.Context) (admin.ClientUpdateStatus, error) {
	s.statusRead = true
	return admin.ClientUpdateStatus{
		Mode:      admin.ClientUpdatesInProcess,
		PublicURL: "https://chat.example/_updates",
		Entries:   []admin.ClientUpdateEntry{{Platform: "linux", Channel: "stable", Build: 6000000, File: "td-update-linux-x64-6000000"}},
	}, nil
}

func TestAdminAPIClientUpdatesReadRequiresUpdatesRead(t *testing.T) {
	svc := &clientUpdatesCaptureService{}
	srv := &Server{
		token: "master",
		scoped: []ScopedToken{
			{Name: "release-viewer", Token: "updates-token", Permissions: []string{PermissionUpdatesRead}},
			{Name: "publisher", Token: "publish-token", Permissions: []string{PermissionUpdatesPublish}},
		},
		svc: svc,
	}

	read := httptest.NewRequest(http.MethodGet, "/v1/client-updates", nil)
	read.Header.Set("Authorization", "Bearer updates-token")
	readResponse := httptest.NewRecorder()
	srv.routes().ServeHTTP(readResponse, read)
	if readResponse.Code != http.StatusOK || !svc.statusRead ||
		!strings.Contains(readResponse.Body.String(), `"mode":"in_process"`) ||
		!strings.Contains(readResponse.Body.String(), `"file":"td-update-linux-x64-6000000"`) {
		t.Fatalf("status=%d read=%v body=%s", readResponse.Code, svc.statusRead, readResponse.Body.String())
	}

	// 发布权限不隐含查看权限。
	svc.statusRead = false
	forbidden := httptest.NewRequest(http.MethodGet, "/v1/client-updates", nil)
	forbidden.Header.Set("Authorization", "Bearer publish-token")
	forbiddenResponse := httptest.NewRecorder()
	srv.routes().ServeHTTP(forbiddenResponse, forbidden)
	if forbiddenResponse.Code != http.StatusForbidden || svc.statusRead ||
		!strings.Contains(forbiddenResponse.Body.String(), PermissionUpdatesRead) {
		t.Fatalf("forbidden status=%d body=%s", forbiddenResponse.Code, forbiddenResponse.Body.String())
	}
}

func TestAdminAPIClientUpdatesReadWithoutServiceSupport(t *testing.T) {
	srv := &Server{token: "master", svc: fakeService{}}
	req := httptest.NewRequest(http.MethodGet, "/v1/client-updates", nil)
	req.Header.Set("Authorization", "Bearer master")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d body=%s, want 501", rec.Code, rec.Body.String())
	}
}
