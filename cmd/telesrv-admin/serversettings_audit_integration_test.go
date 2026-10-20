package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/adminapi"
	"telesrv/internal/identity"
	"telesrv/internal/procctl"
	"telesrv/internal/store/postgres"
)

// 端到端：后台 → 真实的 admin API → 服务端的管理命令 → 服务端的 identity 目录，命令与
// 审计落在 Postgres。只证明各层接通、每次写入恰好留下一条命令和一条审计；每种写操作的
// 细节（试运行、幂等、校验）在 internal/admin 层测。

// serverSettingsChain 起一个接上真实 admin API 的后台。服务端写 serverDir；后台的
// identity 读同一个目录，就像 Docker 下后台以只读方式挂载 server_state 卷。
func serverSettingsChain(t *testing.T) (*server, *readStore, string) {
	t.Helper()
	srv, store := operatorServer(t)
	serverDir := t.TempDir()
	svc := admin.NewService(admin.Dependencies{
		Commands:                postgres.NewAdminStore(store.pool),
		ServerIdentity:          identity.NewStore(serverDir),
		SignUpEnabledDefault:    func() bool { return true },
		CodeLoginEnabledDefault: func() bool { return true },
		PasswordLoginEnabled:    func() bool { return true },
	})
	api := httptest.NewServer(adminapi.NewHandler(adminapi.Config{Token: "chain-token"}, svc, nil))
	t.Cleanup(api.Close)
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = api.URL, "chain-token"
	srv.identity = identity.NewStore(serverDir)
	return srv, store, serverDir
}

// commandRowCounts 返回 command_id 在 admin_commands 与 admin_audit_logs 里各有几行。
func commandRowCounts(t *testing.T, store *readStore, commandID string) (commands, audits int) {
	t.Helper()
	ctx := context.Background()
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM admin_commands WHERE command_id = $1`, commandID).Scan(&commands); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM admin_audit_logs WHERE command_id = $1`, commandID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	return commands, audits
}

func deleteCommandRows(store *readStore, commandID string) {
	ctx := context.Background()
	_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, commandID)
	_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, commandID)
}

func TestServerSettingsWritesAreAudited(t *testing.T) {
	srv, store, serverDir := serverSettingsChain(t)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".env.example"), []byte("# group\nTELESRV_FOO=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.envCtl = procctl.NewManager(repo)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	cookies, csrf := signIn(t, srv)

	jsonPost := func(path, body string) *http.Request {
		req := withCookies(httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)), cookies)
		req.Header.Set(csrfHeaderName, csrf)
		return req
	}
	iconPost := func(metadata string) *http.Request {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("metadata", metadata)
		fw, _ := mw.CreateFormFile("file", "icon.png")
		_, _ = fw.Write([]byte("\x89PNG fake icon bytes"))
		_ = mw.Close()
		req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/upload-server-icon", &buf), cookies)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set(csrfHeaderName, csrf)
		return req
	}
	// 后台读到的是服务端写进去的内容：经后台自己的只读接口取回。
	panelIdentity := func(t *testing.T) serverIdentityAPIResponse {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/server/identity", nil), cookies))
		var out serverIdentityAPIResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("GET /api/server/identity status=%d body=%s", rec.Code, rec.Body.String())
		}
		return out
	}
	panelIconStatus := func() int {
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/server/icon", nil), cookies))
		return rec.Code
	}

	cases := []struct {
		action string
		build  func(commandID string) *http.Request
		check  func(t *testing.T)
	}{
		{"server.set_identity", func(id string) *http.Request {
			return jsonPost("/api/actions/set-server-identity", fmt.Sprintf(`{"command_id":%q,"reason":"rebrand","confirm":true,"name":"Acme","description":"d"}`, id))
		}, func(t *testing.T) {
			if info := panelIdentity(t); info.Name != "Acme" || info.Description != "d" {
				t.Fatalf("identity not written: %+v", info)
			}
		}},
		{"server.set_welcome_message_templates", func(id string) *http.Request {
			return jsonPost("/api/actions/set-welcome-message-templates", fmt.Sprintf(`{"command_id":%q,"reason":"copy edit","confirm":true,"phone_template":"hi","email_template":""}`, id))
		}, func(t *testing.T) {
			if info := panelIdentity(t); info.WelcomeMessagePhoneTemplate != "hi" {
				t.Fatalf("welcome template not written: %+v", info)
			}
		}},
		{"server.set_login_code_message_template", func(id string) *http.Request {
			return jsonPost("/api/actions/set-login-code-message-template", fmt.Sprintf(`{"command_id":%q,"reason":"copy edit","confirm":true,"template":"code {{code}}"}`, id))
		}, func(t *testing.T) {
			if info := panelIdentity(t); info.LoginCodeMessageTemplate != "code {{code}}" {
				t.Fatalf("login code template not written: %+v", info)
			}
		}},
		{"server.set_sign_up_enabled", func(id string) *http.Request {
			return jsonPost("/api/actions/set-sign-up-enabled", fmt.Sprintf(`{"command_id":%q,"reason":"spam wave","confirm":true,"mode":"off"}`, id))
		}, func(t *testing.T) {
			if info := panelIdentity(t); info.SignUpEnabled == nil || *info.SignUpEnabled || info.SignUpSource != "override" {
				t.Fatalf("sign-up override not written: %+v", info)
			}
		}},
		{"server.set_code_login_enabled", func(id string) *http.Request {
			return jsonPost("/api/actions/set-code-login-enabled", fmt.Sprintf(`{"command_id":%q,"reason":"password only","confirm":true,"mode":"off"}`, id))
		}, func(t *testing.T) {
			if info := panelIdentity(t); info.CodeLoginEnabled == nil || *info.CodeLoginEnabled || info.CodeLoginEnabledEffective || info.CodeLoginSource != "override" {
				t.Fatalf("code login override not written: %+v", info)
			}
		}},
		{"server.upload_icon", func(id string) *http.Request {
			return iconPost(fmt.Sprintf(`{"command_id":%q,"reason":"logo","confirm":true}`, id))
		}, func(t *testing.T) {
			if code := panelIconStatus(); code != http.StatusOK {
				t.Fatalf("icon not readable by the panel: status=%d", code)
			}
		}},
		{"server.remove_icon", func(id string) *http.Request {
			return jsonPost("/api/actions/remove-server-icon", fmt.Sprintf(`{"command_id":%q,"reason":"logo gone","confirm":true}`, id))
		}, func(t *testing.T) {
			if code := panelIconStatus(); code != http.StatusNotFound {
				t.Fatalf("icon still served after removal: status=%d", code)
			}
		}},
		// .env 是后台自己的文件，仍由后台本地写入并由后台的命令执行器审计。
		{"server.update_env", func(id string) *http.Request {
			return jsonPost("/api/actions/update-server-env", fmt.Sprintf(`{"command_id":%q,"reason":"tune","confirm":true,"values":{"TELESRV_FOO":"s3cret-value"}}`, id))
		}, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(repo, ".env"))
			if err != nil || !strings.Contains(string(data), "TELESRV_FOO=s3cret-value") {
				t.Fatalf(".env not written: %q err=%v", data, err)
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			commandID := "srvset-" + strings.ReplaceAll(tc.action, ".", "-") + "-" + suffix
			t.Cleanup(func() { deleteCommandRows(store, commandID) })
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, tc.build(commandID))
			var result admin.CommandResult
			_ = json.Unmarshal(rec.Body.Bytes(), &result)
			if rec.Code != http.StatusOK || result.Error != "" || result.Action != tc.action {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			tc.check(t)

			// 恰好一条命令、一条审计：没有两边各记一条，也没有没人记。
			if commands, audits := commandRowCounts(t, store, commandID); commands != 1 || audits != 1 {
				t.Fatalf("command rows=%d audit rows=%d, want exactly one of each", commands, audits)
			}
			rows, err := store.listAuditLogs(ctx, "admin", tc.action, "", 200)
			if err != nil {
				t.Fatal(err)
			}
			var row *auditLogAPIEntry
			for i := range rows {
				if rows[i].CommandID == commandID {
					row = &rows[i]
				}
			}
			if row == nil || row.DryRun || row.Status != "completed" || row.Reason == "" || row.CreatedAt.IsZero() {
				t.Fatalf("audit row for %s = %+v", commandID, row)
			}
			// Env values can be secrets: the trail records which keys changed,
			// never their values.
			var stored string
			if err := store.pool.QueryRow(ctx, `SELECT request::text || result::text FROM admin_commands WHERE command_id = $1`, commandID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(stored, "s3cret-value") {
				t.Fatalf("audit trail leaked an env value: %s", stored)
			}
		})
	}

	// 所有 identity 文件都在服务端的目录里。
	if _, err := os.Stat(filepath.Join(serverDir, "identity.json")); err != nil {
		t.Fatalf("identity.json not in the server's directory: %v", err)
	}
}
