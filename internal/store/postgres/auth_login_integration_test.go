package postgres

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	appauth "telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

func TestAuthSignUpWritesOfficialLoginMessagePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// 5c06299e 起手机号必须是合法 E.164（NANP 共 11 位）；纳秒全量拼接会超长。
	phone := fmt.Sprintf("1555%07d", time.Now().UnixNano()%10_000_000)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE phone = $1", phone)
	})

	users := NewUserStore(pool)
	dialogs := NewDialogStore(pool)
	messages := NewMessageStore(pool)
	svc := appauth.NewService(
		users,
		NewAuthorizationStore(pool),
		memory.NewCodeStore(),
		nil,
		nil,
		"12345",
		appauth.WithLoginMessages(messages, dialogs),
		appauth.WithLoginCodeDelivery(messages),
	)

	var authKeyID [8]byte
	var authKeyBody [256]byte
	if _, err := rand.Read(authKeyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(authKeyBody[:]); err != nil {
		t.Fatal(err)
	}
	if err := NewAuthKeyStore(pool).Save(ctx, store.AuthKeyData{ID: authKeyID, Value: authKeyBody}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM auth_keys WHERE auth_key_id = $1", authKeyIDToInt64(authKeyID))
	})
	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode: %v", err)
	}
	if _, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345"); err != nil || !needSignUp {
		t.Fatalf("SignIn needSignUp = %v err = %v, want need sign-up", needSignUp, err)
	}

	u, msg, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "PgLogin", "Test")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if u.Phone != phone || msg.ID == 0 || !strings.Contains(msg.Body, "Login code: 12345") {
		t.Fatalf("sign-up user/message = user %+v message %+v, want login message", u, msg)
	}

	systemUser, found, err := users.ByID(ctx, domain.OfficialSystemUserID)
	if err != nil || !found || !systemUser.Verified || !systemUser.Support {
		t.Fatalf("official system user = %+v found=%v err=%v, want seeded verified support user", systemUser, found, err)
	}
	list, err := dialogs.ListByUser(ctx, u.ID, domain.DialogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list.Dialogs) != 1 || list.Dialogs[0].Peer.ID != domain.OfficialSystemUserID {
		t.Fatalf("dialogs = %+v, want official login dialog", list.Dialogs)
	}
	// 0133dc7b 起 SignUp 之后还会无条件写一条 777000 欢迎消息，因此官方会话的
	// top 是欢迎消息，历史里同时保留注册时的登录码消息（与 app/auth 内存测试一致）。
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}
	history, err := messages.ListByUser(ctx, u.ID, domain.MessageFilter{HasPeer: true, Peer: peer, Limit: 10})
	if err != nil {
		t.Fatalf("ListByUser messages: %v", err)
	}
	var loginMsg, welcomeMsg domain.Message
	for _, m := range history.Messages {
		if strings.Contains(m.Body, "Login code: 12345") {
			loginMsg = m
		} else {
			welcomeMsg = m
		}
	}
	if len(history.Messages) != 2 || loginMsg.ID != msg.ID || welcomeMsg.ID <= msg.ID {
		t.Fatalf("history = %+v, want returned login message %d + later welcome message", history.Messages, msg.ID)
	}
	if len(list.Messages) != 1 || list.Messages[0].ID != welcomeMsg.ID ||
		list.Dialogs[0].TopMessage != welcomeMsg.ID || list.Dialogs[0].UnreadCount != 2 {
		t.Fatalf("dialog = %+v messages = %+v, want welcome top %d with 2 unread", list.Dialogs[0], list.Messages, welcomeMsg.ID)
	}
	if len(list.Users) != 1 || list.Users[0].ID != domain.OfficialSystemUserID || !list.Users[0].Verified || !list.Users[0].Support {
		t.Fatalf("users = %+v, want official support user", list.Users)
	}
}

func TestAuthSendCodeOfficialLoginMessagePreservesReadWatermarkBeforeSignInPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// 5c06299e 起手机号必须是合法 E.164（NANP 共 11 位）；纳秒全量拼接会超长。
	phone := fmt.Sprintf("1555%07d", time.Now().UnixNano()%10_000_000)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE phone = $1", phone)
	})

	users := NewUserStore(pool)
	dialogs := NewDialogStore(pool)
	messages := NewMessageStore(pool)
	svc := appauth.NewService(
		users,
		NewAuthorizationStore(pool),
		memory.NewCodeStore(),
		nil,
		nil,
		"12345",
		appauth.WithLoginMessages(messages, dialogs),
		appauth.WithLoginCodeDelivery(messages),
	)

	var authKeyID [8]byte
	var authKeyBody [256]byte
	if _, err := rand.Read(authKeyID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(authKeyBody[:]); err != nil {
		t.Fatal(err)
	}
	if err := NewAuthKeyStore(pool).Save(ctx, store.AuthKeyData{ID: authKeyID, Value: authKeyBody}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM auth_keys WHERE auth_key_id = $1", authKeyIDToInt64(authKeyID))
	})

	hash, err := svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode signup: %v", err)
	}
	if _, _, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345"); err != nil || !needSignUp {
		t.Fatalf("SignIn before signup needSignUp=%v err=%v, want true/nil", needSignUp, err)
	}
	u, first, err := svc.SignUp(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "PgLogin", "Read")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}
	read, err := messages.ReadHistory(ctx, domain.ReadHistoryRequest{
		OwnerUserID: u.ID,
		Peer:        peer,
		MaxID:       domain.MaxMessageBoxID,
		Date:        int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("ReadHistory first login message: %v", err)
	}
	// 0133dc7b 起每次登录成功都会在登录码之后追加一条 777000 欢迎消息：
	// SignUp 后官方历史为 登录码(first) + 欢迎(first+1)，整段已读落在欢迎消息上。
	welcomeID := first.ID + 1
	if read.MaxID != welcomeID || read.StillUnreadCount != 0 {
		t.Fatalf("read first login message = %+v, want max_id %d unread 0", read, welcomeID)
	}

	assertOfficialDialog := func(wantTop, wantRead, wantUnread int) {
		t.Helper()
		var top, readMax, unread, computed int
		if err := pool.QueryRow(ctx, `
WITH target AS (
  SELECT top_message_id, read_inbox_max_id, unread_count
  FROM dialogs
  WHERE user_id = $1 AND peer_type = 'user' AND peer_id = $2
)
SELECT
  target.top_message_id,
  target.read_inbox_max_id,
  target.unread_count,
  (
    SELECT count(*)::int
    FROM message_boxes m
    WHERE m.owner_user_id = $1
      AND m.peer_type = 'user'
      AND m.peer_id = $2
      AND NOT m.outgoing
      AND NOT m.deleted
      AND m.box_id > target.read_inbox_max_id
  ) AS computed_unread
FROM target`, u.ID, domain.OfficialSystemUserID).Scan(&top, &readMax, &unread, &computed); err != nil {
			t.Fatalf("query official dialog state: %v", err)
		}
		if top != wantTop || readMax != wantRead || unread != wantUnread || computed != wantUnread {
			t.Fatalf("official dialog top=%d read=%d unread=%d computed=%d, want top=%d read=%d unread=%d",
				top, readMax, unread, computed, wantTop, wantRead, wantUnread)
		}
	}

	hash, err = svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode signin second: %v", err)
	}
	// 核心时序：SendCode 返回时登录码消息/会话/未读已提交，尚未 SignIn。
	secondID := welcomeID + 1
	assertOfficialDialog(secondID, welcomeID, 1)
	_, lateSecond, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345")
	if err != nil || needSignUp {
		t.Fatalf("SignIn second needSignUp=%v err=%v", needSignUp, err)
	}
	if lateSecond.ID != 0 {
		t.Fatalf("SignIn second returned late login message %+v, want zero", lateSecond)
	}
	// SignIn 只追加欢迎消息，从不补发迟到的登录码；已读水位保持不动。
	assertOfficialDialog(secondID+1, welcomeID, 2)

	hash, err = svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode signin third: %v", err)
	}
	thirdID := secondID + 2
	assertOfficialDialog(thirdID, welcomeID, 3)
	_, lateThird, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345")
	if err != nil || needSignUp {
		t.Fatalf("SignIn third needSignUp=%v err=%v", needSignUp, err)
	}
	if lateThird.ID != 0 {
		t.Fatalf("SignIn third returned late login message %+v, want zero", lateThird)
	}
	assertOfficialDialog(thirdID+1, welcomeID, 4)
}
