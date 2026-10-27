package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAccountPermissionsAPIFillsWhitelistMembers 读取账号权限时，面板服务端按 send_whitelist.user_ids
// 一次批量读库补全 members：按 user_ids 的顺序给出 id、名、姓、@用户名、手机号和 bot 标记；
// 库里已不存在的 id 也保留一项（found=false，只有 id），名单里的人在面板上始终能看到并移除。
func TestAccountPermissionsAPIFillsWhitelistMembers(t *testing.T) {
	read, pool := verificationReadStore(t)
	ctx := context.Background()
	base := 3_700_000_000 + randomBelow(t, 1_000_000)*10
	human, bot, missing := base+1, base+2, base+3
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int64{human, bot})
	})
	phone := fmt.Sprintf("1888%d", base)
	if _, err := pool.Exec(ctx, `
INSERT INTO users (id, access_hash, phone, first_name, last_name, username, is_bot, created_at, updated_at)
VALUES ($1, $1, $2, 'Ada', 'Lovelace', $3, false, now(), now()),
       ($4, $4, '', 'Helper', '', $5, true, now(), now())`,
		human, phone, fmt.Sprintf("ada%d", base), bot, fmt.Sprintf("helper%dbot", base)); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"user_id":1001,"found":true,"enabled":true,"send_whitelist":{"enabled":true,"user_ids":[%d,%d,%d]}}`, human, bot, missing)
	}))
	defer upstream.Close()
	srv, err := newServer(uiConfig{SessionKey: []byte(testSessionKey), Password: "letmein", Permissions: []string{permissionAccountsRead}}, read, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.cfg.AdminAPIURL, srv.cfg.AdminAPIToken = upstream.URL, "test-admin"
	cookies, _ := signIn(t, srv)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, withCookies(httptest.NewRequest(http.MethodGet, "/api/accounts/1001/permissions", nil), cookies))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := fmt.Sprintf(`{"user_id":1001,"found":true,"enabled":true,"send_whitelist":{"enabled":true,"user_ids":[%[1]d,%[2]d,%[3]d],"members":[
		{"id":%[1]d,"found":true,"first_name":"Ada","last_name":"Lovelace","username":"ada%[4]d","phone":"%[5]s","bot":false},
		{"id":%[2]d,"found":true,"first_name":"Helper","last_name":"","username":"helper%[4]dbot","phone":"","bot":true},
		{"id":%[3]d,"found":false,"first_name":"","last_name":"","username":"","phone":"","bot":false}]}}`,
		human, bot, missing, base, phone)
	if !jsonEqual(t, rec.Body.String(), want) {
		t.Fatalf("body=%s\nwant=%s", rec.Body.String(), want)
	}
}
