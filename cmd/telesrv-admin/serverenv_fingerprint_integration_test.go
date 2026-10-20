package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/procctl"
)

// The .env editor's command_id fingerprint binds the values without storing
// them. An unsalted digest of key=value would be an offline oracle for a
// secret: anyone who can read admin_commands could hash guesses and compare.
// The stored fingerprint must therefore not be derivable from the value alone,
// while a replay of the same request (same panel session key) still reports
// already-executed rather than a conflict.
func TestServerEnvFingerprintIsKeyedNotAPlainValueDigest(t *testing.T) {
	srv, store := operatorServer(t)
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".env.example"), []byte("# group\nTELESRV_FOO=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.envCtl = procctl.NewManager(repo)
	ctx := context.Background()
	cookies, csrf := signIn(t, srv)
	commandID := fmt.Sprintf("srvenv-fp-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, commandID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, commandID)
	})
	post := func() admin.CommandResult {
		body := fmt.Sprintf(`{"command_id":%q,"reason":"rotate","confirm":true,"values":{"TELESRV_FOO":"s3cret-value"}}`, commandID)
		req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/update-server-env", strings.NewReader(body)), cookies)
		req.Header.Set(csrfHeaderName, csrf)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		var result admin.CommandResult
		_ = json.Unmarshal(rec.Body.Bytes(), &result)
		if rec.Code != http.StatusOK || result.Error != "" {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		return result
	}
	post()

	var stored operatorCommandRequest
	var raw []byte
	if err := store.pool.QueryRow(ctx, `SELECT request FROM admin_commands WHERE command_id = $1`, commandID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	plain := sha256.Sum256([]byte("TELESRV_FOO=s3cret-value\x00"))
	plainHex := hex.EncodeToString(plain[:])
	if strings.Contains(string(raw), plainHex) {
		t.Fatalf("request stores the unsalted value digest: %s", raw)
	}
	// The runner fingerprints the params as json.Marshal emits them (the
	// stored jsonb copy is re-spaced by Postgres), so rebuild that form.
	params, _ := json.Marshal(map[string]any{"keys": []string{"TELESRV_FOO"}})
	if stored.Fingerprint == operatorCommandFingerprint("server.update_env", params, plainHex) {
		t.Fatalf("fingerprint is reproducible from the value alone: %s", stored.Fingerprint)
	}

	if replay := post(); replay.Error != "" || replay.Status != "completed" {
		t.Fatalf("replay = %+v, want the first run's completed result", replay)
	}
}
