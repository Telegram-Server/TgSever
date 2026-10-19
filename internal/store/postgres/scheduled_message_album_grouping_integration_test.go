package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestScheduledMessageAlbumGroupedIDRoundTrip 验证定时消息的相册 grouped_id 在建、列、取、
// 改、认领（手动与到期）、删各路径都原样往返。门控于 TELESRV_TEST_POSTGRES_DSN。
func TestScheduledMessageAlbumGroupedIDRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 171, Phone: "+1782" + suffix + "01", FirstName: "AlbumScheduledOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	peerUser, err := users.Create(ctx, domain.User{AccessHash: 172, Phone: "+1782" + suffix + "02", FirstName: "AlbumScheduledPeer"})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	ids := []int64{owner.ID, peerUser.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM scheduled_messages WHERE owner_user_id = $1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", ids)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", ids)
	})

	messages := NewMessageStore(pool)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: peerUser.ID}
	const groupedID = int64(7766554433221100)
	const scheduleDate = 1700003600
	created := make([]domain.ScheduledMessage, 0, 3)
	for i, randomID := range []int64{8101, 8102, 8103} {
		media := &domain.MessageMedia{
			Kind:     domain.MessageMediaKindDocument,
			Document: &domain.Document{ID: 910000000000000201 + int64(i), AccessHash: 9201, DCID: 2, MimeType: "image/jpeg"},
		}
		msg, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
			OwnerUserID:  owner.ID,
			Peer:         peer,
			RandomID:     randomID,
			Media:        media,
			GroupedID:    groupedID,
			ScheduleDate: scheduleDate,
			Date:         1700000000,
		})
		if err != nil {
			t.Fatalf("create scheduled album item %d: %v", i, err)
		}
		if msg.GroupedID != groupedID {
			t.Fatalf("created item %d grouped_id = %d, want %d", i, msg.GroupedID, groupedID)
		}
		created = append(created, msg)
	}
	assertGrouped := func(label string, items []domain.ScheduledMessage, want int) {
		t.Helper()
		if len(items) != want {
			t.Fatalf("%s len = %d, want %d (%+v)", label, len(items), want, items)
		}
		for _, item := range items {
			if item.GroupedID != groupedID {
				t.Fatalf("%s item %d grouped_id = %d, want %d", label, item.ID, item.GroupedID, groupedID)
			}
		}
	}

	list, err := messages.ListScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner.ID, Peer: peer})
	if err != nil {
		t.Fatalf("list scheduled: %v", err)
	}
	assertGrouped("list", list.Messages, 3)
	got, err := messages.GetScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner.ID, Peer: peer, IDs: []int{created[0].ID, created[1].ID}})
	if err != nil {
		t.Fatalf("get scheduled: %v", err)
	}
	assertGrouped("get", got.Messages, 2)

	edited, err := messages.EditScheduledMessage(ctx, domain.EditScheduledMessageRequest{
		OwnerUserID: owner.ID, Peer: peer, ID: created[0].ID,
		SetMessage: true, Message: "caption", ScheduleDate: scheduleDate, Date: 1700000100,
	})
	if err != nil {
		t.Fatalf("edit scheduled: %v", err)
	}
	assertGrouped("edit", []domain.ScheduledMessage{edited}, 1)

	claimed, err := messages.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{
		OwnerUserID: owner.ID, Peer: peer, IDs: []int{created[0].ID}, Now: 1700000200, LeaseUntil: 1700000260,
	})
	if err != nil {
		t.Fatalf("claim scheduled: %v", err)
	}
	assertGrouped("claim", claimed, 1)

	due, err := messages.ClaimDueScheduledMessages(ctx, scheduleDate, 100, 60)
	if err != nil {
		t.Fatalf("claim due scheduled: %v", err)
	}
	ownDue := make([]domain.ScheduledMessage, 0, len(due))
	for _, item := range due {
		if item.OwnerUserID == owner.ID {
			ownDue = append(ownDue, item)
		}
	}
	// 手动认领的那条 lease 已在 scheduleDate 之前过期，到期认领一并回收：三条全在。
	assertGrouped("claim due", ownDue, 3)

	deleted, err := messages.DeleteScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner.ID, Peer: peer, IDs: []int{created[2].ID}}, 1700000300)
	if err != nil {
		t.Fatalf("delete scheduled: %v", err)
	}
	assertGrouped("delete", deleted, 1)

	single, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
		OwnerUserID: owner.ID, Peer: peer, RandomID: 8104, Message: "single",
		ScheduleDate: scheduleDate + 60, Date: 1700000000,
	})
	if err != nil {
		t.Fatalf("create single scheduled: %v", err)
	}
	if single.GroupedID != 0 {
		t.Fatalf("single scheduled grouped_id = %d, want 0", single.GroupedID)
	}
}
