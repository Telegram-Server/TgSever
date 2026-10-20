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
)

// 注册开关的每次改动都经后台转发给服务端执行，由服务端的命令执行器记录：谁、何时、
// 为什么、是否试运行，并按 command_id 幂等。这里走完整链路（后台 → 真实 admin API →
// 服务端命令 → Postgres），证明预览、重放和冲突在跨进程之后仍然成立。

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
	srv, store, _ := serverSettingsChain(t)
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
			deleteCommandRows(store, id)
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
	t.Cleanup(func() { deleteCommandRows(store, dry.CommandID) })
	if got := override(); got != nil {
		t.Fatalf("dry run wrote override %v", *got)
	}
	if row := auditRow(dry.CommandID); !row.DryRun || row.Reason != "spam wave" {
		t.Fatalf("dry run audit row=%+v", row)
	}

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
	if commands, audits := commandRowCounts(t, store, ids["off"]); commands != 1 || audits != 1 {
		t.Fatalf("after replay and conflict: command rows=%d audit rows=%d, want one of each", commands, audits)
	}

	// Off again, then on: opening sign-up goes through the same chain.
	if err := srv.identity.SetSignUpOverride(nil); err != nil {
		t.Fatal(err)
	}
	if code, res := postSignUpToggle(t, srv, cookies, csrf,
		fmt.Sprintf(`{"command_id":%q,"reason":"wave over","confirm":true,"mode":"on"}`, ids["on"])); code != http.StatusOK || res.Error != "" {
		t.Fatalf("on status=%d result=%+v", code, res)
	}
	if got := override(); got == nil || !*got {
		t.Fatalf("override after on = %v, want true", got)
	}
	if row := auditRow(ids["on"]); row.Reason != "wave over" || row.DryRun {
		t.Fatalf("on audit row=%+v", row)
	}

	// Clear back to following the env.
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
}
