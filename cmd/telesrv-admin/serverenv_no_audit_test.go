package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/admin"
	"telesrv/internal/identity"
	"telesrv/internal/procctl"
)

// The .env editor is how an operator repairs a broken TELESRV_POSTGRES_DSN, and
// the break-glass login works without the database precisely for that. So when
// the audit store is missing or unreachable the .env write still goes through,
// and the result says no audit row was recorded. The identity writes are not
// local at all: the server executes and audits them (see serversettings.go).
func TestServerEnvUpdateWorksWithoutTheAuditStore(t *testing.T) {
	unreachable := func(t *testing.T) *readStore {
		pool, err := pgxpool.New(context.Background(), "postgres://nobody:x@127.0.0.1:1/none_test?sslmode=disable&connect_timeout=2")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return newReadStore(pool)
	}
	cases := []struct {
		name string
		read func(t *testing.T) *readStore
	}{
		{"not configured", func(*testing.T) *readStore { return nil }},
		{"unreachable", unreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := panelServer(t, permissionAll)
			srv.read = tc.read(t)
			repo := t.TempDir()
			if err := os.WriteFile(filepath.Join(repo, ".env.example"), []byte("# group\nTELESRV_POSTGRES_DSN=\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			srv.envCtl = procctl.NewManager(repo)
			cookies, csrf := signIn(t, srv)

			post := func(body string) (int, admin.CommandResult) {
				req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/update-server-env", strings.NewReader(body)), cookies)
				req.Header.Set(csrfHeaderName, csrf)
				rec := httptest.NewRecorder()
				srv.routes().ServeHTTP(rec, req)
				var result admin.CommandResult
				_ = json.Unmarshal(rec.Body.Bytes(), &result)
				return rec.Code, result
			}

			code, result := post(`{"command_id":"fix-dsn-1","reason":"repair dsn","confirm":true,"values":{"TELESRV_POSTGRES_DSN":"postgres://fixed"}}`)
			if code != http.StatusOK || result.Error != "" || result.Status != "completed" {
				t.Fatalf("status=%d result=%+v", code, result)
			}
			if recorded, ok := result.Details["audit_recorded"].(bool); !ok || recorded {
				t.Fatalf("result must say no audit was recorded: %+v", result.Details)
			}
			data, err := os.ReadFile(filepath.Join(repo, ".env"))
			if err != nil || !strings.Contains(string(data), "TELESRV_POSTGRES_DSN=postgres://fixed") {
				t.Fatalf(".env not written: %q err=%v", data, err)
			}

			// A dry run still writes nothing.
			code, result = post(`{"command_id":"dry-fix-dsn-2","reason":"repair dsn","confirm":false,"values":{"TELESRV_POSTGRES_DSN":"postgres://other"}}`)
			if code != http.StatusOK || !result.DryRun || result.Error != "" {
				t.Fatalf("dry run status=%d result=%+v", code, result)
			}
			if data, _ := os.ReadFile(filepath.Join(repo, ".env")); strings.Contains(string(data), "postgres://other") {
				t.Fatalf("dry run wrote .env: %q", data)
			}

			// A reason is still required, as for an audited write.
			if code, _ := post(`{"command_id":"fix-dsn-3","reason":"","confirm":true,"values":{"TELESRV_POSTGRES_DSN":"postgres://x"}}`); code == http.StatusOK {
				t.Fatalf("write without a reason was accepted")
			}
		})
	}
}

// Identity writes are not a repair path and not this process's to make: they
// are executed (and audited) by the server. With the server's admin API
// unreachable they fail, and the panel does not fall back to writing the
// identity directory itself.
func TestServerIdentityWriteNeedsTheServer(t *testing.T) {
	srv := panelServer(t, permissionAll)
	srv.cfg.AdminAPIURL = "http://127.0.0.1:1"
	dir := t.TempDir()
	srv.identity = identity.NewStore(dir)
	cookies, csrf := signIn(t, srv)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-server-identity",
		strings.NewReader(`{"command_id":"id-1","reason":"rebrand","confirm":true,"name":"Acme","description":""}`)), cookies)
	req.Header.Set(csrfHeaderName, csrf)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("identity write without the server status=%d body=%s, want 502", rec.Code, rec.Body.String())
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("panel fell back to a local identity write: %v err=%v", entries, err)
	}
}
