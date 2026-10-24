package postgres

import (
	"context"
	"fmt"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/pgtest"
	"telesrv/internal/store/storetest"
)

// TestScheduledMessagesContractPostgres 跑定时消息共享契约。ClaimDueScheduledMessages 是
// 全局扫描，契约断言"恰好认领哪几行"，所以独占一个库，不受同包其它测试残留行干扰，
// 也不会改写别人的租约。
func TestScheduledMessagesContractPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := Open(ctx, pgtest.NewDatabase(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	users := NewUserStore(pool)
	ids := make([]int64, 0, 3)
	for i, name := range []string{"ScheduledOwner", "ScheduledOther", "ScheduledPeer"} {
		u, err := users.Create(ctx, domain.User{AccessHash: int64(171 + i), Phone: fmt.Sprintf("+178200000%02d", i), FirstName: name})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		ids = append(ids, u.ID)
	}
	storetest.ScheduledMessages(t, NewMessageStore(pool), ids[0], ids[1], ids[2], 1999000000)
}

// TestScheduledMessageColumnsRoundTrip 只断言 postgres 专有的列：契约看不到的
// lease_until / last_error / sent_message_id，以及私聊 dialogs.has_scheduled 标志随
// 队列增减维护。
func TestScheduledMessageColumnsRoundTrip(t *testing.T) {
	pool := testTx(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 181, Phone: "+1783" + suffix + "01", FirstName: "ColumnsOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	peerUser, err := users.Create(ctx, domain.User{AccessHash: 182, Phone: "+1783" + suffix + "02", FirstName: "ColumnsPeer"})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}

	messages := NewMessageStore(pool)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: peerUser.ID}
	hasScheduled := func(t *testing.T) bool {
		t.Helper()
		var has bool
		if err := pool.QueryRow(ctx, "SELECT has_scheduled FROM dialogs WHERE user_id = $1 AND peer_type = 'user' AND peer_id = $2", owner.ID, peerUser.ID).Scan(&has); err != nil {
			t.Fatalf("read dialogs.has_scheduled: %v", err)
		}
		return has
	}
	type columns struct {
		state         string
		leaseUntil    int
		lastError     string
		sentMessageID int
	}
	readColumns := func(t *testing.T, id int) columns {
		t.Helper()
		var c columns
		if err := pool.QueryRow(ctx, `SELECT state, lease_until, last_error, sent_message_id FROM scheduled_messages
WHERE owner_user_id = $1 AND scheduled_id = $2`, owner.ID, id).Scan(&c.state, &c.leaseUntil, &c.lastError, &c.sentMessageID); err != nil {
			t.Fatalf("read scheduled columns: %v", err)
		}
		return c
	}

	row, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
		OwnerUserID: owner.ID, Peer: peer, RandomID: 8301, Message: "columns", ScheduleDate: 1700003600, Date: 1700000000,
	})
	if err != nil {
		t.Fatalf("create scheduled: %v", err)
	}
	if !hasScheduled(t) {
		t.Fatalf("dialogs.has_scheduled = false after create, want true")
	}
	if _, err := messages.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{
		OwnerUserID: owner.ID, Peer: peer, IDs: []int{row.ID}, Now: 1700000100, LeaseUntil: 1700000160,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if got := readColumns(t, row.ID); got != (columns{state: "dispatching", leaseUntil: 1700000160}) {
		t.Fatalf("claimed columns = %+v, want dispatching with lease", got)
	}
	if err := messages.ReleaseScheduledMessage(ctx, owner.ID, row.ID, "boom"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if got := readColumns(t, row.ID); got != (columns{state: "pending", lastError: "boom"}) {
		t.Fatalf("released columns = %+v, want pending, lease cleared, last_error kept", got)
	}
	if !hasScheduled(t) {
		t.Fatalf("dialogs.has_scheduled = false while still queued")
	}
	if err := messages.MarkScheduledMessageSent(ctx, owner.ID, row.ID, 4242, 1700000200); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if got := readColumns(t, row.ID); got != (columns{state: "sent", lastError: "boom", sentMessageID: 4242}) {
		t.Fatalf("sent columns = %+v, want sent with sent_message_id", got)
	}
	if hasScheduled(t) {
		t.Fatalf("dialogs.has_scheduled = true after the only row was sent, want false")
	}
}
