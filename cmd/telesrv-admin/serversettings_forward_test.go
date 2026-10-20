package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"telesrv/internal/admin"
	"telesrv/internal/identity"
)

// 服务器设置的 6 个写接口不再在后台进程里写 identity 目录（Docker 下它是只读的），
// 而是把命令经 admin API 交给主服务器执行。这里用假 admin API 证明：每个接口转发了
// 正确的命令（command_id、操作者、理由、试运行标记、参数）、结果原样回给前端、本地
// 目录一个字节都不写、权限依旧是 server.manage。命令本身的行为在 internal/admin 测。

type forwardedServerSetting struct {
	Path     string
	Auth     string
	Body     map[string]any
	FileName string
	File     []byte
}

type fakeServerSettingsAPI struct {
	mu    sync.Mutex
	calls []forwardedServerSetting
	// fail 让上游回答 400 + COMMAND_ID_CONFLICT，用来检查失败结果的透传。
	fail bool
}

func (f *fakeServerSettingsAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := forwardedServerSetting{Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		var raw []byte
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			if err := r.ParseMultipartForm(8 << 20); err != nil {
				t.Errorf("upstream multipart: %v", err)
			}
			raw = []byte(r.FormValue("metadata"))
			if file, header, err := r.FormFile("file"); err == nil {
				call.FileName = header.Filename
				call.File, _ = io.ReadAll(file)
				_ = file.Close()
			}
		} else {
			raw, _ = io.ReadAll(r.Body)
		}
		if err := json.Unmarshal(raw, &call.Body); err != nil {
			t.Errorf("upstream body %q: %v", raw, err)
		}
		f.mu.Lock()
		f.calls = append(f.calls, call)
		fail := f.fail
		f.mu.Unlock()
		commandID, _ := call.Body["command_id"].(string)
		dryRun, _ := call.Body["dry_run"].(bool)
		if fail {
			writeJSON(w, http.StatusBadRequest, admin.CommandResult{CommandID: commandID, Status: "failed", Error: "COMMAND_ID_CONFLICT", Message: "command_id is already bound to a different request"})
			return
		}
		writeJSON(w, http.StatusOK, admin.CommandResult{
			CommandID: commandID, Action: "upstream:" + r.URL.Path, DryRun: dryRun, Status: "completed",
			Message: "applied by telesrv", Details: map[string]any{"from": "server"},
		})
	})
}

func (f *fakeServerSettingsAPI) snapshot() []forwardedServerSetting {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]forwardedServerSetting(nil), f.calls...)
}

// forwardingPanel 是接上假 admin API 的后台；identity 指向一个空的临时目录，用来证明
// 后台不再往本地写。
func forwardingPanel(t *testing.T, permissions ...string) (*server, *fakeServerSettingsAPI, string) {
	t.Helper()
	upstream := &fakeServerSettingsAPI{}
	api := httptest.NewServer(upstream.handler(t))
	t.Cleanup(api.Close)
	srv := panelServer(t, permissions...)
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = api.URL, "test-admin"
	dir := t.TempDir()
	srv.identity = identity.NewStore(dir)
	return srv, upstream, dir
}

var testServerIcon = []byte("\x89PNG\r\n\x1a\npanel icon")

func serverIconForm(t *testing.T, metadata, fileName string, data []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("metadata", metadata)
	fw, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

type serverSettingWrite struct {
	name, path, upstream string
	body                 string
	icon                 bool
	// params 是除命令元数据外必须原样转发的参数。
	params map[string]any
}

func serverSettingWrites() []serverSettingWrite {
	return []serverSettingWrite{
		{"identity", "/api/actions/set-server-identity", "/v1/server-settings/set-identity",
			`{"command_id":"exec-1","reason":"rebrand","confirm":true,"name":"Acme","description":"d"}`, false,
			map[string]any{"name": "Acme", "description": "d"}},
		{"welcome templates", "/api/actions/set-welcome-message-templates", "/v1/server-settings/set-welcome-message-templates",
			`{"command_id":"exec-1","reason":"rebrand","confirm":true,"phone_template":"hi","email_template":""}`, false,
			map[string]any{"phone_template": "hi", "email_template": ""}},
		{"login-code template", "/api/actions/set-login-code-message-template", "/v1/server-settings/set-login-code-message-template",
			`{"command_id":"exec-1","reason":"rebrand","confirm":true,"template":"code {{code}}"}`, false,
			map[string]any{"template": "code {{code}}"}},
		{"sign-up", "/api/actions/set-sign-up-enabled", "/v1/server-settings/set-sign-up-enabled",
			`{"command_id":"exec-1","reason":"rebrand","confirm":true,"mode":"off"}`, false,
			map[string]any{"mode": "off"}},
		{"code login", "/api/actions/set-code-login-enabled", "/v1/server-settings/set-code-login-enabled",
			`{"command_id":"exec-1","reason":"rebrand","confirm":true,"mode":"follow_env"}`, false,
			map[string]any{"mode": "follow_env"}},
		{"upload icon", "/api/actions/upload-server-icon", "/v1/server-settings/upload-icon",
			`{"command_id":"exec-1","reason":"rebrand","confirm":true}`, true, nil},
		{"remove icon", "/api/actions/remove-server-icon", "/v1/server-settings/remove-icon",
			`{"command_id":"exec-1","reason":"rebrand","confirm":true}`, false, nil},
	}
}

func (c serverSettingWrite) request(t *testing.T, body string, cookies []*http.Cookie, csrf string) *http.Request {
	t.Helper()
	var req *http.Request
	if c.icon {
		form, contentType := serverIconForm(t, body, "Logo.png", testServerIcon)
		req = httptest.NewRequest(http.MethodPost, c.path, form)
		req.Header.Set("Content-Type", contentType)
	} else {
		req = httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(body))
	}
	req = withCookies(req, cookies)
	if csrf != "" {
		req.Header.Set(csrfHeaderName, csrf)
	}
	return req
}

func TestServerSettingsWritesAreForwardedToTheServer(t *testing.T) {
	for _, c := range serverSettingWrites() {
		t.Run(c.name, func(t *testing.T) {
			srv, upstream, dir := forwardingPanel(t, permissionServerManage)
			cookies, csrf := signIn(t, srv)

			// 正式执行：命令原样交给 admin API，结果原样回给前端。
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, c.request(t, c.body, cookies, csrf))
			var result admin.CommandResult
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil || rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
			}
			if result.CommandID != "exec-1" || result.Action != "upstream:"+c.upstream || result.Message != "applied by telesrv" || result.Details["from"] != "server" {
				t.Fatalf("panel result = %+v, want the server's result passed through", result)
			}
			calls := upstream.snapshot()
			if len(calls) != 1 {
				t.Fatalf("upstream calls = %d, want 1", len(calls))
			}
			call := calls[0]
			if call.Path != c.upstream || call.Auth != "Bearer test-admin" {
				t.Fatalf("forwarded to %s with %q, want %s with the admin API token", call.Path, call.Auth, c.upstream)
			}
			if call.Body["command_id"] != "exec-1" || call.Body["actor"] != "admin" || call.Body["reason"] != "rebrand" || call.Body["dry_run"] != false {
				t.Fatalf("forwarded command meta = %+v", call.Body)
			}
			for key, want := range c.params {
				if call.Body[key] != want {
					t.Fatalf("forwarded %s = %#v, want %#v (body %+v)", key, call.Body[key], want, call.Body)
				}
			}
			if c.icon && (call.FileName != "Logo.png" || !bytes.Equal(call.File, testServerIcon)) {
				t.Fatalf("forwarded icon %q (%q), want Logo.png with the uploaded bytes", call.FileName, call.File)
			}

			// 预览：confirm=false 变成 dry_run=true，后台自动生成 dry- 前缀的 command_id。
			preview := strings.Replace(strings.Replace(c.body, `"confirm":true`, `"confirm":false`, 1), `"command_id":"exec-1",`, "", 1)
			rec = httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, c.request(t, preview, cookies, csrf))
			if rec.Code != http.StatusOK {
				t.Fatalf("preview status=%d body=%s", rec.Code, rec.Body.String())
			}
			calls = upstream.snapshot()
			if len(calls) != 2 || calls[1].Body["dry_run"] != true || !strings.HasPrefix(calls[1].Body["command_id"].(string), "dry-") {
				t.Fatalf("preview forwarded as %+v, want a dry run with a dry- command id", calls[len(calls)-1].Body)
			}

			// 后台不再写本地的 identity 目录。
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatalf("panel wrote its own identity dir: %v err=%v", entries, err)
			}
		})
	}
}

func TestServerSettingsWriteFailuresArePassedThrough(t *testing.T) {
	for _, c := range serverSettingWrites() {
		t.Run(c.name, func(t *testing.T) {
			srv, upstream, _ := forwardingPanel(t, permissionServerManage)
			upstream.fail = true
			cookies, csrf := signIn(t, srv)
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, c.request(t, c.body, cookies, csrf))
			var result admin.CommandResult
			_ = json.Unmarshal(rec.Body.Bytes(), &result)
			if rec.Code != http.StatusBadGateway || result.Error != "COMMAND_ID_CONFLICT" || result.Status != "failed" {
				t.Fatalf("status=%d body=%s, want 502 carrying the server's failure", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestServerSettingsWritesRequireServerManage(t *testing.T) {
	for _, c := range serverSettingWrites() {
		t.Run(c.name, func(t *testing.T) {
			srv, upstream, _ := forwardingPanel(t, permissionAccountsManage)
			cookies, csrf := signIn(t, srv)
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, c.request(t, c.body, cookies, csrf))
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), permissionServerManage) {
				t.Fatalf("without server.manage status=%d body=%s", rec.Code, rec.Body.String())
			}
			anonymous := httptest.NewRecorder()
			srv.routes().ServeHTTP(anonymous, c.request(t, c.body, nil, ""))
			if anonymous.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous status=%d", anonymous.Code)
			}
			if calls := upstream.snapshot(); len(calls) != 0 {
				t.Fatalf("refused requests reached the admin API: %+v", calls)
			}
		})
	}
}

// 后台仍在转发之前挡掉明显不合法的输入，响应码和以前一样（422 / 400），不打扰服务端；
// 服务端自己也做同样的校验（internal/admin）。
func TestServerSettingsRejectInvalidInputBeforeForwarding(t *testing.T) {
	srv, upstream, _ := forwardingPanel(t, permissionServerManage)
	cookies, csrf := signIn(t, srv)
	post := func(path, body string) int {
		req := withCookies(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)), cookies)
		req.Header.Set(csrfHeaderName, csrf)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post("/api/actions/set-login-code-message-template", `{"reason":"r","confirm":true,"template":"no placeholder"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("login-code template without {{code}} status=%d, want 422", code)
	}
	if code := post("/api/actions/set-sign-up-enabled", `{"reason":"r","confirm":true,"mode":"maybe"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown sign-up mode status=%d, want 422", code)
	}
	if code := post("/api/actions/set-code-login-enabled", `{"reason":"r","confirm":true,"mode":"maybe"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown code login mode status=%d, want 422", code)
	}
	form, contentType := serverIconForm(t, `{"reason":"r","confirm":true}`, "logo.bmp", testServerIcon)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/upload-server-icon", form), cookies)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(csrfHeaderName, csrf)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported icon extension status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if calls := upstream.snapshot(); len(calls) != 0 {
		t.Fatalf("invalid input reached the admin API: %+v", calls)
	}
}
