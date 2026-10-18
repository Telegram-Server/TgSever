package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ListMessageDeletions and MessageDeletionDetail are hand-written SQL over
// message_deletions, so the filters, the cursor and the snapshot/device
// decoding are proven against the real schema.
func TestReadStoreMessageDeletions(t *testing.T) {
	store, pool := verificationReadStore(t)
	ctx := context.Background()
	// actor/owner/sender ids unique to this run, so other rows in the shared
	// test database never match the filters below.
	base := 3_800_000_000 + time.Now().UnixNano()%1_000_000*10
	alice, bob := base+1, base+2
	channelID := base + 4
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM message_deletions WHERE sender_user_id = ANY($1::bigint[])`, []int64{alice, bob})
	})

	insert := func(batch int64, source, scope, actorType string, actor, owner int64, peerType string, peerID int64, msgID int, sender int64, date int, body string, device string, operator, command, reason string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
INSERT INTO message_deletions (
	batch_id, deleted_date, source, scope, actor_type, actor_user_id,
	origin_auth_key_id, origin_session_id, origin_device,
	operator_id, command_id, reason,
	owner_user_id, peer_type, peer_id, message_id, private_message_id,
	sender_user_id, message_date, snapshot
) VALUES ($1,$2,$3,$4,$5,$6, 77, 88, $7::jsonb, $8,$9,$10, $11,$12,$13,$14, 0, $15, $16, jsonb_build_object('body', $17::text))`,
			batch, date, source, scope, actorType, actor, device, operator, command, reason,
			owner, peerType, peerID, msgID, sender, date-10, body); err != nil {
			t.Fatalf("seed deletion: %v", err)
		}
	}
	phone := `{"device_model":"Pixel 9","platform":"android","ip":"203.0.113.7"}`
	// alice revokes bob's message: two copies in one batch.
	insert(base, "user", "both", "user", alice, alice, "user", bob, 11, bob, 1_700_200_001, "hello alice", phone, "", "", "")
	insert(base, "user", "both", "user", alice, bob, "user", alice, 12, bob, 1_700_200_001, "hello alice", phone, "", "", "")
	// alice deletes her own message in a group.
	insert(base+1, "user", "everyone", "user", alice, 0, "channel", channelID, 5, alice, 1_700_200_002, "group typo", phone, "", "", "")
	// a moderator removes bob's group message.
	insert(base+2, "moderation", "everyone", "operator", 0, 0, "channel", channelID, 6, bob, 1_700_200_003, "reported", `{}`, "reviewer", "cmd-6", "moderation case 6")

	t.Run("by actor", func(t *testing.T) {
		rows, err := store.ListMessageDeletions(ctx, MessageDeletionQuery{ActorUserID: alice, Limit: 10})
		if err != nil {
			t.Fatalf("ListMessageDeletions: %v", err)
		}
		if len(rows) != 3 || rows[0].MessageID != 5 || rows[0].PeerType != "channel" {
			t.Fatalf("rows = %+v, want alice's 3 deletions newest first", rows)
		}
		if rows[0].DeviceModel != "Pixel 9" || rows[0].IP != "203.0.113.7" || rows[0].OriginSessionID != 88 {
			t.Fatalf("row device = %+v, want the snapshot from deletion time", rows[0])
		}
	})

	t.Run("by sender and source", func(t *testing.T) {
		rows, err := store.ListMessageDeletions(ctx, MessageDeletionQuery{SenderUserID: bob, Source: "moderation", Limit: 10})
		if err != nil {
			t.Fatalf("ListMessageDeletions: %v", err)
		}
		if len(rows) != 1 || rows[0].OperatorID != "reviewer" || rows[0].CommandID != "cmd-6" || rows[0].Reason != "moderation case 6" || rows[0].Body != "reported" {
			t.Fatalf("rows = %+v, want the moderation deletion of bob's message", rows)
		}
	})

	t.Run("by owner and peer", func(t *testing.T) {
		rows, err := store.ListMessageDeletions(ctx, MessageDeletionQuery{OwnerUserID: bob, PeerType: "user", PeerID: alice, Limit: 10})
		if err != nil {
			t.Fatalf("ListMessageDeletions: %v", err)
		}
		if len(rows) != 1 || rows[0].MessageID != 12 || rows[0].Scope != "both" {
			t.Fatalf("rows = %+v, want bob's copy of the revoked message", rows)
		}
		rows, err = store.ListMessageDeletions(ctx, MessageDeletionQuery{PeerType: "channel", PeerID: channelID, MessageID: 6, Limit: 10})
		if err != nil {
			t.Fatalf("ListMessageDeletions by message: %v", err)
		}
		if len(rows) != 1 || rows[0].SenderUserID != bob {
			t.Fatalf("rows = %+v, want group message 6", rows)
		}
	})

	t.Run("date range and cursor", func(t *testing.T) {
		rows, err := store.ListMessageDeletions(ctx, MessageDeletionQuery{SenderUserID: bob, MinDate: 1_700_200_001, MaxDate: 1_700_200_002, Limit: 10})
		if err != nil {
			t.Fatalf("ListMessageDeletions dates: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("rows = %+v, want the two revoked copies inside the range", rows)
		}
		first, err := store.ListMessageDeletions(ctx, MessageDeletionQuery{ActorUserID: alice, Limit: 1})
		if err != nil || len(first) != 1 {
			t.Fatalf("first page = %+v err %v", first, err)
		}
		next, err := store.ListMessageDeletions(ctx, MessageDeletionQuery{ActorUserID: alice, BeforeID: first[0].ID, Limit: 10})
		if err != nil {
			t.Fatalf("next page: %v", err)
		}
		if len(next) != 2 || next[0].ID >= first[0].ID {
			t.Fatalf("next page = %+v, want the 2 older rows", next)
		}
	})

	t.Run("detail carries the snapshot and the rest of the batch", func(t *testing.T) {
		rows, err := store.ListMessageDeletions(ctx, MessageDeletionQuery{OwnerUserID: alice, PeerID: bob, Limit: 1})
		if err != nil || len(rows) != 1 {
			t.Fatalf("seed lookup = %+v err %v", rows, err)
		}
		detail, err := store.MessageDeletionDetail(ctx, rows[0].ID)
		if err != nil {
			t.Fatalf("MessageDeletionDetail: %v", err)
		}
		if detail.Deletion.ID != rows[0].ID || !strings.Contains(detail.SnapshotJSON, `"hello alice"`) {
			t.Fatalf("detail = %+v, want the row and its snapshot", detail)
		}
		if len(detail.Batch) != 2 {
			t.Fatalf("batch = %+v, want both copies revoked together", detail.Batch)
		}
	})
}
