package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"telesrv/internal/admin"
)

// 服务器设置命令的路由与鉴权。功能矩阵在 internal/admin 层；这里只证明每条路由解出
// 正确的请求交给 Service、结果原样返回，并且只有主 token 能调用。

type serverSettingsCaptureService struct {
	fakeService
	calls []any
}

func (s *serverSettingsCaptureService) record(req any, meta admin.CommandMeta, action string) (admin.CommandResult, error) {
	s.calls = append(s.calls, req)
	return admin.CommandResult{CommandID: meta.CommandID, Action: action, DryRun: meta.DryRun, Status: "completed"}, nil
}

func (s *serverSettingsCaptureService) SetServerIdentity(_ context.Context, req admin.SetServerIdentityRequest) (admin.CommandResult, error) {
	return s.record(req, req.CommandMeta, admin.ActionSetServerIdentity)
}

func (s *serverSettingsCaptureService) SetWelcomeMessageTemplates(_ context.Context, req admin.SetWelcomeMessageTemplatesRequest) (admin.CommandResult, error) {
	return s.record(req, req.CommandMeta, admin.ActionSetWelcomeMessageTemplates)
}

func (s *serverSettingsCaptureService) SetLoginCodeMessageTemplate(_ context.Context, req admin.SetLoginCodeMessageTemplateRequest) (admin.CommandResult, error) {
	return s.record(req, req.CommandMeta, admin.ActionSetLoginCodeMessageTemplate)
}

func (s *serverSettingsCaptureService) SetSignUpEnabled(_ context.Context, req admin.SetSignUpEnabledRequest) (admin.CommandResult, error) {
	return s.record(req, req.CommandMeta, admin.ActionSetSignUpEnabled)
}

func (s *serverSettingsCaptureService) SetCodeLoginEnabled(_ context.Context, req admin.SetCodeLoginEnabledRequest) (admin.CommandResult, error) {
	return s.record(req, req.CommandMeta, admin.ActionSetCodeLoginEnabled)
}

func (s *serverSettingsCaptureService) UploadServerIcon(_ context.Context, req admin.UploadServerIconRequest) (admin.CommandResult, error) {
	return s.record(req, req.CommandMeta, admin.ActionUploadServerIcon)
}

func (s *serverSettingsCaptureService) RemoveServerIcon(_ context.Context, req admin.RemoveServerIconRequest) (admin.CommandResult, error) {
	return s.record(req, req.CommandMeta, admin.ActionRemoveServerIcon)
}

func iconUploadRequest(t *testing.T, metadata string, fileName string, data []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("metadata", metadata); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(data)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/server-settings/upload-icon", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestAdminAPIServerSettingsRoutes(t *testing.T) {
	meta := admin.CommandMeta{CommandID: "c-srv", Actor: "ops", Reason: "rebrand", DryRun: true}
	const metaJSON = `"command_id":"c-srv","actor":"ops","reason":"rebrand","dry_run":true`
	icon := []byte("\x89PNG\r\n\x1a\nicon")
	cases := []struct {
		name   string
		build  func(t *testing.T) *http.Request
		action string
		want   any
	}{
		{"set identity", func(*testing.T) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/server-settings/set-identity",
				strings.NewReader(`{`+metaJSON+`,"name":"Acme","description":"d"}`))
		}, admin.ActionSetServerIdentity, admin.SetServerIdentityRequest{CommandMeta: meta, Name: "Acme", Description: "d"}},
		{"set welcome templates", func(*testing.T) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/server-settings/set-welcome-message-templates",
				strings.NewReader(`{`+metaJSON+`,"phone_template":"hi","email_template":"mail"}`))
		}, admin.ActionSetWelcomeMessageTemplates, admin.SetWelcomeMessageTemplatesRequest{CommandMeta: meta, PhoneTemplate: "hi", EmailTemplate: "mail"}},
		{"set login-code template", func(*testing.T) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/server-settings/set-login-code-message-template",
				strings.NewReader(`{`+metaJSON+`,"template":"code {{code}}"}`))
		}, admin.ActionSetLoginCodeMessageTemplate, admin.SetLoginCodeMessageTemplateRequest{CommandMeta: meta, Template: "code {{code}}"}},
		{"set sign-up", func(*testing.T) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/server-settings/set-sign-up-enabled",
				strings.NewReader(`{`+metaJSON+`,"mode":"off"}`))
		}, admin.ActionSetSignUpEnabled, admin.SetSignUpEnabledRequest{CommandMeta: meta, Mode: "off"}},
		{"set code login", func(*testing.T) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/server-settings/set-code-login-enabled",
				strings.NewReader(`{`+metaJSON+`,"mode":"follow_env"}`))
		}, admin.ActionSetCodeLoginEnabled, admin.SetCodeLoginEnabledRequest{CommandMeta: meta, Mode: "follow_env"}},
		{"upload icon", func(t *testing.T) *http.Request {
			return iconUploadRequest(t, `{`+metaJSON+`}`, "logo.png", icon)
		}, admin.ActionUploadServerIcon, admin.UploadServerIconRequest{CommandMeta: meta, FileName: "logo.png", Data: icon}},
		{"remove icon", func(*testing.T) *http.Request {
			return httptest.NewRequest(http.MethodPost, "/v1/server-settings/remove-icon", strings.NewReader(`{`+metaJSON+`}`))
		}, admin.ActionRemoveServerIcon, admin.RemoveServerIconRequest{CommandMeta: meta}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &serverSettingsCaptureService{}
			srv := &Server{token: "secret", svc: svc, scoped: []ScopedToken{{Name: "bounded", Token: "scoped", Permissions: []string{PermissionPremiumManage}}}}

			anonymous := httptest.NewRecorder()
			srv.routes().ServeHTTP(anonymous, tc.build(t))
			if anonymous.Code != http.StatusUnauthorized {
				t.Fatalf("without a token status=%d, want 401", anonymous.Code)
			}
			scoped := tc.build(t)
			scoped.Header.Set("Authorization", "Bearer scoped")
			forbidden := httptest.NewRecorder()
			srv.routes().ServeHTTP(forbidden, scoped)
			if forbidden.Code != http.StatusForbidden {
				t.Fatalf("scoped token status=%d, want 403", forbidden.Code)
			}
			if len(svc.calls) != 0 {
				t.Fatalf("unauthorised requests reached the service: %+v", svc.calls)
			}

			req := tc.build(t)
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, req)
			var result admin.CommandResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
			}
			if result.CommandID != "c-srv" || result.Action != tc.action || !result.DryRun {
				t.Fatalf("result=%+v, want the service result passed through", result)
			}
			if len(svc.calls) != 1 {
				t.Fatalf("service calls=%d, want 1", len(svc.calls))
			}
			got, _ := json.Marshal(svc.calls[0])
			want, _ := json.Marshal(tc.want)
			if string(got) != string(want) {
				t.Fatalf("decoded request = %s, want %s", got, want)
			}
			if upload, ok := svc.calls[0].(admin.UploadServerIconRequest); ok && !bytes.Equal(upload.Data, icon) {
				t.Fatalf("uploaded bytes = %q, want %q", upload.Data, icon)
			}
		})
	}
}

func TestAdminAPIServerIconUploadIsBounded(t *testing.T) {
	svc := &serverSettingsCaptureService{}
	srv := &Server{token: "secret", svc: svc}
	tooBig := bytes.Repeat([]byte{1}, admin.MaxServerIconBytes+1)
	for name, req := range map[string]*http.Request{
		"too large":    iconUploadRequest(t, `{"command_id":"c","actor":"ops","reason":"r"}`, "logo.png", tooBig),
		"no file":      httptest.NewRequest(http.MethodPost, "/v1/server-settings/upload-icon", strings.NewReader(`{}`)),
		"bad metadata": iconUploadRequest(t, `{"unknown":1}`, "logo.png", []byte("x")),
	} {
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status=%d body=%s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if len(svc.calls) != 0 {
		t.Fatalf("rejected uploads reached the service: %d", len(svc.calls))
	}
}

// 不提供服务器设置命令的 Service（旧测试替身）得到 501，而不是 panic。
func TestAdminAPIServerSettingsUnavailable(t *testing.T) {
	srv := &Server{token: "secret", svc: fakeService{}}
	req := httptest.NewRequest(http.MethodPost, "/v1/server-settings/set-sign-up-enabled",
		strings.NewReader(`{"command_id":"c","actor":"ops","reason":"r","mode":"off"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d body=%s, want 501", rec.Code, rec.Body.String())
	}
}
