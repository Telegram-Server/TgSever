package postgres

import (
	"context"
	"crypto/rand"
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

	// testPool clones a database per test, so a fixed E.164 number cannot collide.
	phone := "15555550131"

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
	// Completing the sign-up appends the login welcome notice right after the
	// code message, so the dialog's top message is the welcome, not the code.
	if len(list.Messages) != 1 || list.Messages[0].ID != msg.ID+1 || !strings.Contains(list.Messages[0].Body, "Welcome to") {
		t.Fatalf("messages = %+v, want the welcome notice after login message %d", list.Messages, msg.ID)
	}
	if len(list.Users) != 1 || list.Users[0].ID != domain.OfficialSystemUserID || !list.Users[0].Verified || !list.Users[0].Support {
		t.Fatalf("users = %+v, want official support user", list.Users)
	}
}

func TestAuthSendCodeOfficialLoginMessagePreservesReadWatermarkBeforeSignInPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	phone := "15555550132"

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
	// The sign-up's welcome notice follows the code message, and reading the
	// whole history moves the watermark past both.
	readID := first.ID + 1
	if read.MaxID != readID || read.StillUnreadCount != 0 {
		t.Fatalf("read first login messages = %+v, want max_id %d unread 0", read, readID)
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
	// Each SendCode appends a code message and each completed SignIn appends a
	// welcome notice; neither may move the read watermark the user set.
	secondCodeID := readID + 1
	assertOfficialDialog(secondCodeID, readID, 1)
	_, lateSecond, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345")
	if err != nil || needSignUp {
		t.Fatalf("SignIn second needSignUp=%v err=%v", needSignUp, err)
	}
	if lateSecond.ID != 0 {
		t.Fatalf("SignIn second returned late login message %+v, want zero", lateSecond)
	}
	secondWelcomeID := secondCodeID + 1
	assertOfficialDialog(secondWelcomeID, readID, 2)

	hash, err = svc.SendCode(ctx, phone)
	if err != nil {
		t.Fatalf("SendCode signin third: %v", err)
	}
	thirdCodeID := secondWelcomeID + 1
	assertOfficialDialog(thirdCodeID, readID, 3)
	_, lateThird, needSignUp, err := svc.SignIn(ctx, domain.Authorization{AuthKeyID: authKeyID}, phone, hash, "12345")
	if err != nil || needSignUp {
		t.Fatalf("SignIn third needSignUp=%v err=%v", needSignUp, err)
	}
	if lateThird.ID != 0 {
		t.Fatalf("SignIn third returned late login message %+v, want zero", lateThird)
	}
	assertOfficialDialog(thirdCodeID+1, readID, 4)
}
