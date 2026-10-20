package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/identity"
)

// Every sign-up switch change is audited through the panel's own command
// runner: who, when, why, dry run or not, replayable by command_id.

func postSignUpToggle(t *testing.T, srv *server, cookies []*http.Cookie, csrf, body string) (int, admin.CommandResult) {
	t.Helper()
	req := withCookies(httptest.NewRequest(http.MethodPost, "/api/actions/set-sign-up-enabled", strings.NewReader(body)), cookies)
	req.Header.Set(csrfHeaderName, csrf)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	var out admin.CommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode toggle response (status %d): %v body=%s", rec.Code, err, rec.Body.String())
	}
	return rec.Code, out
}

func TestSetSignUpEnabledTogglesOverrideAndAuditsEveryChange(t *testing.T) {
	srv, store := operatorServer(t)
	srv.identity = identity.NewStore(t.TempDir())
	srv.cfg.SignUpEnabledDefault = true
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	cookies, csrf := signIn(t, srv)
	const action = "server.set_sign_up_enabled"

	ids := map[string]string{
		"off":    "signup-off-" + suffix,
		"on":     "signup-on-" + suffix,
		"follow": "signup-follow-" + suffix,
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, id)
			_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, id)
		}
	})
	override := func() *bool {
		t.Helper()
		info, err := srv.identity.Get()
		if err != nil {
			t.Fatal(err)
		}
		return info.SignUpEnabled
	}
	auditRow := func(commandID string) auditLogAPIEntry {
		t.Helper()
		rows, err := store.listAuditLogs(ctx, "admin", action, "", 200)
		if err != nil {
			t.Fatalf("list audit logs: %v", err)
		}
		for _, row := range rows {
			if row.CommandID == commandID {
				return row
			}
		}
		t.Fatalf("no audit row for %s", commandID)
		return auditLogAPIEntry{}
	}

	// Dry run: nothing changes, but the preview is still on the trail.
	code, dry := postSignUpToggle(t, srv, cookies, csrf, `{"reason":"spam wave","confirm":false,"mode":"off"}`)
	if code != http.StatusOK || !dry.DryRun || dry.Error != "" || dry.Action != action {
		t.Fatalf("dry run status=%d result=%+v", code, dry)
	}
	if got := override(); got != nil {
		t.Fatalf("dry run wrote override %v", *got)
	}
	if row := auditRow(dry.CommandID); !row.DryRun || row.Reason != "spam wave" {
		t.Fatalf("dry run audit row=%+v", row)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_audit_logs WHERE command_id = $1`, dry.CommandID)
		_, _ = store.pool.Exec(ctx, `DELETE FROM admin_commands WHERE command_id = $1`, dry.CommandID)
	})

	// Off.
	code, off := postSignUpToggle(t, srv, cookies, csrf,
		fmt.Sprintf(`{"command_id":%q,"reason":"spam wave","confirm":true,"mode":"off"}`, ids["off"]))
	if code != http.StatusOK || off.DryRun || off.Error != "" || off.Status != "completed" {
		t.Fatalf("off status=%d result=%+v", code, off)
	}
	if got := override(); got == nil || *got {
		t.Fatalf("override after off = %v, want false", got)
	}
	row := auditRow(ids["off"])
	if row.DryRun || row.Actor != "admin" || row.Reason != "spam wave" || row.Status != "completed" || row.CreatedAt.IsZero() {
		t.Fatalf("off audit row=%+v", row)
	}

	// Replaying the same command_id returns the stored outcome without running
	// again: flip the file behind its back and check the replay leaves it alone.
	on := true
	if err := srv.identity.SetSignUpOverride(&on); err != nil {
		t.Fatal(err)
	}
	code, replay := postSignUpToggle(t, srv, cookies, csrf,
		fmt.Sprintf(`{"command_id":%q,"reason":"spam wave","confirm":true,"mode":"off"}`, ids["off"]))
	if code != http.StatusOK || !replay.AlreadyExecuted {
		t.Fatalf("replay status=%d result=%+v", code, replay)
	}
	if got := override(); got == nil || !*got {
		t.Fatalf("replay re-executed: override=%v", got)
	}
	// The same command_id bound to a different mode is a conflict.
	if code, conflict := postSignUpToggle(t, srv, cookies, csrf,
		fmt.Sprintf(`{"command_id":%q,"reason":"spam wave","confirm":true,"mode":"on"}`, ids["off"])); code == http.StatusOK || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("conflict status=%d result=%+v", code, conflict)
	}

	// On, then clear back to following the env.
	if code, res := postSignUpToggle(t, srv, cookies, csrf,
		fmt.Sprintf(`{"command_id":%q,"reason":"wave over","confirm":true,"mode":"on"}`, ids["on"])); code != http.StatusOK || res.Error != "" {
		t.Fatalf("on status=%d result=%+v", code, res)
	}
	if got := override(); got == nil || !*got {
		t.Fatalf("override after on = %v, want true", got)
	}
	if code, res := postSignUpToggle(t, srv, cookies, csrf,
		fmt.Sprintf(`{"command_id":%q,"reason":"back to deploy config","confirm":true,"mode":"follow_env"}`, ids["follow"])); code != http.StatusOK || res.Error != "" {
		t.Fatalf("follow_env status=%d result=%+v", code, res)
	}
	if got := override(); got != nil {
		t.Fatalf("override after follow_env = %v, want cleared", *got)
	}
	if row := auditRow(ids["follow"]); row.Reason != "back to deploy config" || row.DryRun {
		t.Fatalf("follow_env audit row=%+v", row)
	}
	if row := auditRow(ids["on"]); row.Reason != "wave over" {
		t.Fatalf("on audit row=%+v", row)
	}
}
