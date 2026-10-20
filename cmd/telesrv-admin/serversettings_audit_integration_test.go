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
	"telesrv/internal/identity"
	"telesrv/internal/procctl"
)

// Every Server Settings write -- not just the sign-up switch -- goes through
// the panel's command runner, so each one lands in the audit trail with the
// operator, the reason and the time, dry run or not.
func TestServerSettingsWritesAreAudited(t *testing.T) {
	srv, store := operatorServer(t)
	srv.identity = identity.NewStore(t.TempDir())
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

	cases := []struct {
		action string
		build  func(commandID string) *http.Request
		check  func(t *testing.T)
	}{
		{"server.set_identity", func(id string) *http.Request {
			return jsonPost("/api/actions/set-server-identity", fmt.Sprintf(`{"command_id":%q,"reason":"rebrand","confirm":true,"name":"Acme","description":"d"}`, id))
		}, func(t *testing.T) {
			if info, _ := srv.identity.Get(); info.Name != "Acme" {
				t.Fatalf("identity not written: %+v", info)
			}
		}},
		{"server.set_welcome_message_templates", func(id string) *http.Request {
			return jsonPost("/api/actions/set-welcome-message-templates", fmt.Sprintf(`{"command_id":%q,"reason":"copy edit","confirm":true,"phone_template":"hi","email_template":""}`, id))
		}, func(t *testing.T) {
			if info, _ := srv.identity.Get(); info.WelcomeMessagePhoneTemplate != "hi" {
				t.Fatalf("welcome template not written: %+v", info)
			}
		}},
		{"server.set_login_code_message_template", func(id string) *http.Request {
			return jsonPost("/api/actions/set-login-code-message-template", fmt.Sprintf(`{"command_id":%q,"reason":"copy edit","confirm":true,"template":"code {{code}}"}`, id))
		}, func(t *testing.T) {
			if info, _ := srv.identity.Get(); info.LoginCodeMessageTemplate != "code {{code}}" {
				t.Fatalf("login code template not written: %+v", info)
			}
		}},
		{"server.upload_icon", func(id string) *http.Request {
			return iconPost(fmt.Sprintf(`{"command_id":%q,"reason":"logo","confirm":true}`, id))
		}, func(t *testing.T) {
			if _, ext, ok := srv.identity.Icon(); !ok || ext != ".png" {
				t.Fatalf("icon not written: ok=%v ext=%q", ok, ext)
			}
		}},
		{"server.remove_icon", func(id string) *http.Request {
			return jsonPost("/api/actions/remove-server-icon", fmt.Sprintf(`{"command_id":%q,"reason":"logo gone","confirm":true}`, id))
		}, func(t *testing.T) {
			if _, _, ok := srv.identity.Icon(); ok {
				t.Fatal("icon still present after removal")
			}
		}},
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
			t.Cleanup(func() {
				_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, commandID)
				_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, commandID)
			})
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, tc.build(commandID))
			var result admin.CommandResult
			_ = json.Unmarshal(rec.Body.Bytes(), &result)
			if rec.Code != http.StatusOK || result.Error != "" || result.Action != tc.action {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			tc.check(t)

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
}
