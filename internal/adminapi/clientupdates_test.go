package adminapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"telesrv/internal/admin"
	"telesrv/internal/updatecdn"
)

type clientUpdatesCaptureService struct {
	fakeService
	statusRead bool

	uploads    int
	uploadReq  admin.UploadClientUpdateRequest
	uploadBody []byte
	// uploadBuffered 记录 Service 收到的是不是已经整体读进内存的请求体。
	uploadBuffered bool
	published      []admin.PublishClientUpdateRequest
}

func (s *clientUpdatesCaptureService) UploadClientUpdate(_ context.Context, req admin.UploadClientUpdateRequest, body io.Reader) (admin.CommandResult, error) {
	s.uploads++
	s.uploadReq = req
	switch body.(type) {
	case *bytes.Reader, *bytes.Buffer, *strings.Reader:
		s.uploadBuffered = true
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return admin.CommandResult{}, err
	}
	s.uploadBody = data
	return admin.CommandResult{CommandID: req.CommandID, Action: admin.ActionPreflightClientUpdate, Status: "completed", DryRun: true,
		Details: map[string]any{"staging_id": "0123456789abcdef0123456789abcdef"}}, nil
}

func (s *clientUpdatesCaptureService) PublishClientUpdate(_ context.Context, req admin.PublishClientUpdateRequest) (admin.CommandResult, error) {
	s.published = append(s.published, req)
	return admin.CommandResult{CommandID: req.CommandID, Action: admin.ActionPublishClientUpdate, Status: "completed"}, nil
}

func clientUpdatesServer(svc Service) *Server {
	return &Server{
		token: "master",
		scoped: []ScopedToken{
			{Name: "release-viewer", Token: "updates-token", Permissions: []string{PermissionUpdatesRead}},
			{Name: "publisher", Token: "publish-token", Permissions: []string{PermissionUpdatesPublish}},
		},
		svc: svc,
	}
}

func uploadRequest(token, query string, body io.Reader) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/client-updates/uploads?"+query, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/octet-stream")
	return req
}

func TestAdminAPIClientUpdateUploadStreamsBodyToService(t *testing.T) {
	svc := &clientUpdatesCaptureService{}
	srv := clientUpdatesServer(svc)
	payload := bytes.Repeat([]byte("pkg!"), 64*1024)
	query := url.Values{"command_id": {"up-1"}, "reason": {"ship 6.0.1"}, "allow_downgrade": {"true"}}.Encode()
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, uploadRequest("publish-token", query, bytes.NewReader(payload)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"staging_id":"0123456789abcdef0123456789abcdef"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Equal(svc.uploadBody, payload) || svc.uploadBuffered {
		t.Fatalf("service got %d bytes (buffered=%v), want the %d request bytes as a stream", len(svc.uploadBody), svc.uploadBuffered, len(payload))
	}
	got := svc.uploadReq
	// 未显式给出 actor 时用作用域令牌的名字。
	if got.CommandID != "up-1" || got.Reason != "ship 6.0.1" || !got.AllowDowngrade || got.Actor != "publisher" || !got.DryRun {
		t.Fatalf("upload request = %+v", got)
	}

	readOnly := httptest.NewRecorder()
	srv.routes().ServeHTTP(readOnly, uploadRequest("updates-token", query, bytes.NewReader(payload)))
	if readOnly.Code != http.StatusForbidden || svc.uploads != 1 || !strings.Contains(readOnly.Body.String(), PermissionUpdatesPublish) {
		t.Fatalf("read-only upload status=%d uploads=%d body=%s", readOnly.Code, svc.uploads, readOnly.Body.String())
	}
}

func TestAdminAPIClientUpdateUploadRejectsMultipartAndOversize(t *testing.T) {
	svc := &clientUpdatesCaptureService{}
	srv := clientUpdatesServer(svc)
	query := url.Values{"command_id": {"up-1"}, "reason": {"r"}}.Encode()

	multipart := uploadRequest("publish-token", query, strings.NewReader("--x\r\n"))
	multipart.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, multipart)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("multipart upload status=%d body=%s, want 415", rec.Code, rec.Body.String())
	}

	oversize := uploadRequest("publish-token", query, strings.NewReader("x"))
	oversize.ContentLength = updatecdn.MaxDesktopPackageSize + 1
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, oversize)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize upload status=%d body=%s, want 413", rec.Code, rec.Body.String())
	}
	if svc.uploads != 0 {
		t.Fatalf("rejected uploads reached the service %d times", svc.uploads)
	}
}

func TestAdminAPIClientUpdatePublishRequiresUpdatesPublish(t *testing.T) {
	svc := &clientUpdatesCaptureService{}
	srv := clientUpdatesServer(svc)
	body := `{"command_id":"exec-1","reason":"ship","staging_id":"0123456789abcdef0123456789abcdef","allow_downgrade":true}`

	req := httptest.NewRequest(http.MethodPost, "/v1/client-updates/publish", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer publish-token")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(svc.published) != 1 {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := svc.published[0]
	if got.StagingID != "0123456789abcdef0123456789abcdef" || !got.AllowDowngrade || got.Actor != "publisher" || got.Reason != "ship" {
		t.Fatalf("publish request = %+v", got)
	}

	forbidden := httptest.NewRequest(http.MethodPost, "/v1/client-updates/publish", strings.NewReader(body))
	forbidden.Header.Set("Authorization", "Bearer updates-token")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, forbidden)
	if rec.Code != http.StatusForbidden || len(svc.published) != 1 {
		t.Fatalf("read-only publish status=%d body=%s", rec.Code, rec.Body.String())
	}
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
