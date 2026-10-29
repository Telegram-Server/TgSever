package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 账号详情设备列表里的推送开关只在会话持有 push.manage 时由服务端返回（不只是前端隐藏）：
// 有 APNs/VoIP 登记的会话带 PushEnabled（全部登记开启才算开启），没有登记的会话不带。
func TestAccountDetailCarriesPushStateOnlyForPushManage(t *testing.T) {
	store, pool := isolatedReadStore(t)
	ctx := context.Background()
	const userID = int64(3_800_000_001)
	const (
		iosOn    = int64(-4_100_000_001) // APNs + VoIP 都开启
		iosMixed = int64(4_100_000_002)  // APNs 开、VoIP 关：算关闭
		desktop  = int64(4_100_000_003)  // 没有推送登记
	)
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, access_hash, phone, first_name, last_name, username, created_at, updated_at)
VALUES ($1, $1, '', 'Push', '', '', now(), now())`, userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for _, key := range []int64{iosOn, iosMixed, desktop} {
		if _, err := pool.Exec(ctx, `INSERT INTO auth_keys (auth_key_id, body, server_salt) VALUES ($1, '\x00', 0)`, key); err != nil {
			t.Fatalf("seed auth key %d: %v", key, err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO authorizations (user_id, auth_key_id, hash, created_at, active_at)
VALUES ($1, $2, $2, now(), now())`, userID, key); err != nil {
			t.Fatalf("seed authorization %d: %v", key, err)
		}
	}
	for _, reg := range []struct {
		key       int64
		tokenType int
		enabled   bool
	}{{iosOn, 1, true}, {iosOn, 9, true}, {iosMixed, 1, true}, {iosMixed, 9, false}} {
		if _, err := pool.Exec(ctx, `
INSERT INTO push_devices (auth_key_id, token_type, token, user_id, push_enabled)
VALUES ($1, $2, $3, $4, $5)`, reg.key, reg.tokenType, fmt.Sprintf("tok-%d-%d", reg.key, reg.tokenType), userID, reg.enabled); err != nil {
			t.Fatalf("seed push device: %v", err)
		}
	}

	detail := func(permissions ...string) map[int64]map[string]any {
		t.Helper()
		srv, err := newServer(uiConfig{SessionKey: []byte(testSessionKey), Password: "letmein", Permissions: permissions}, store, nil)
		if err != nil {
			t.Fatalf("newServer: %v", err)
		}
		cookies, _ := signIn(t, srv)
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/accounts/%d", userID), nil)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, withCookies(req, cookies))
		if rec.Code != http.StatusOK {
			t.Fatalf("account detail status=%d body=%s", rec.Code, rec.Body.String())
		}
		var body struct {
			Authorizations []map[string]any
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode account detail: %v", err)
		}
		out := map[int64]map[string]any{}
		for _, a := range body.Authorizations {
			var key int64
			if _, err := fmt.Sscan(a["AuthKeyID"].(string), &key); err != nil {
				t.Fatalf("auth key id %v: %v", a["AuthKeyID"], err)
			}
			out[key] = a
		}
		if len(out) != 3 {
			t.Fatalf("authorizations = %+v, want 3", body.Authorizations)
		}
		return out
	}

	for key, row := range detail(permissionAccountsRead) {
		if _, ok := row["PushEnabled"]; ok {
			t.Fatalf("session %d without push.manage carries PushEnabled=%v", key, row["PushEnabled"])
		}
	}

	rows := detail(permissionAccountsRead, permissionPushManage)
	if got, ok := rows[iosOn]["PushEnabled"]; !ok || got != true {
		t.Fatalf("all-enabled session PushEnabled=%v (present %v), want true", got, ok)
	}
	if got, ok := rows[iosMixed]["PushEnabled"]; !ok || got != false {
		t.Fatalf("partly disabled session PushEnabled=%v (present %v), want false", got, ok)
	}
	if got, ok := rows[desktop]["PushEnabled"]; ok {
		t.Fatalf("session without push registrations carries PushEnabled=%v, want absent", got)
	}
}
