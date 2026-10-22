package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestPlainPrivateSendHotPathDurableFacts(t *testing.T) {
	pool := testPool(t)
	baseCtx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	sender := createTestUser(t, baseCtx, users, "+1667"+suffix+"01", "HotSender", "")
	recipient := createTestUser(t, baseCtx, users, "+1667"+suffix+"02", "HotRecipient", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(baseCtx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	authKeyID := [8]byte{0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x80}
	result, err := messages.SendPrivateText(baseCtx, domain.SendPrivateTextRequest{
		SenderUserID:           sender.ID,
		RecipientUserID:        recipient.ID,
		RandomID:               250825001,
		Message:                "plain hot path",
		Date:                   1800000001,
		IdempotencyPreflighted: true,
		OriginAuthKeyID:        authKeyID,
		OriginSessionID:        250825,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	if result.SenderMessage.ID != 1 || result.RecipientMessage.ID != 1 ||
		result.SenderMessage.Pts != 1 || result.RecipientMessage.Pts != 1 {
		t.Fatalf("result = %+v, want first box/PTS for both owners", result)
	}

	for _, fact := range []struct {
		name  string
		query string
		want  int
	}{
		{"boxes", `SELECT count(*) FROM message_boxes WHERE private_message_id=$1`, 2},
		{"events", `SELECT count(*) FROM user_update_events e JOIN message_boxes m ON (m.owner_user_id,m.box_id)=(e.user_id,e.message_box_id) WHERE m.private_message_id=$1`, 2},
		{"outbox", `SELECT count(*) FROM dispatch_outbox d JOIN user_update_events e ON (e.user_id,e.pts)=(d.target_user_id,d.pts) JOIN message_boxes m ON (m.owner_user_id,m.box_id)=(e.user_id,e.message_box_id) WHERE m.private_message_id=$1`, 2},
	} {
		var got int
		if err := pool.QueryRow(baseCtx, fact.query, result.SenderMessage.UID).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", fact.name, err)
		}
		if got != fact.want {
			t.Fatalf("%s rows = %d, want %d", fact.name, got, fact.want)
		}
	}

	var excludeAuthKeyID, excludeSessionID int64
	if err := pool.QueryRow(baseCtx, `
SELECT exclude_auth_key_id, exclude_session_id
FROM dispatch_outbox
WHERE target_user_id=$1`, sender.ID).Scan(&excludeAuthKeyID, &excludeSessionID); err != nil {
		t.Fatalf("load sender exclusion: %v", err)
	}
	if excludeAuthKeyID != authKeyIDToInt64(authKeyID) || excludeSessionID != 250825 {
		t.Fatalf("sender exclusion = %d/%d, want exact auth key/session", excludeAuthKeyID, excludeSessionID)
	}
}
