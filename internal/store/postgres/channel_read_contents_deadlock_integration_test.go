package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
)

// TestChannelReadMessageContentsAndReadHistoryDoNotDeadlock 复现同一用户
// channels.readMessageContents 与 channels.readHistory 并发时的 40P01。
//
// 每用户 advisory 锁在 telesrv_bump_dialog_light 触发器里于语句末拿，规则是
// 一个事务里 advisory(u) 必须是 u 名下最后一把锁。旧的 readHistory 先 UPDATE
// channel_members(u)（语句末拿到 advisory(u)），推进发送者 read_outbox 之后才
// upsert channel_dialogs(u)；readMessageContents 先锁 dialog(u) 行再等 advisory(u)，
// 于是成环。这里用第三个事务 L 锁住发送者的 channel_members 行，把 readHistory
// 卡在推进 read_outbox 处，确定性地排出这个交错。
//
// readMessageContents 调不带重试的 Once 版本，重试兜底不会掩盖死锁。
func TestChannelReadMessageContentsAndReadHistoryDoNotDeadlock(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 811, Phone: "+1778" + suffix + "51", FirstName: "ContentsDeadlockSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	reader, err := users.Create(ctx, domain.User{AccessHash: 812, Phone: "+1778" + suffix + "52", FirstName: "ContentsDeadlockReader", Username: "contents_deadlock_" + suffix})
	if err != nil {
		t.Fatalf("create reader: %v", err)
	}
	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: sender.ID,
		Title:         "Contents Deadlock " + suffix,
		Megagroup:     true,
		MemberUserIDs: []int64{reader.ID},
		Date:          1700000801,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID

	// 先读一条普通消息，让 reader 有 dialog 行；随后的 mention 消息处于未读。
	plain, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: sender.ID, ChannelID: channelID, RandomID: 98101, Message: "plain", Date: 1700000802,
	})
	if err != nil {
		t.Fatalf("send plain: %v", err)
	}
	if _, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
		UserID: reader.ID, ChannelID: channelID, MaxID: plain.Message.ID, Date: 1700000803,
	}); err != nil {
		t.Fatalf("read plain: %v", err)
	}
	mention, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: sender.ID, ChannelID: channelID, RandomID: 98102, Message: "hi", MentionUserIDs: []int64{reader.ID}, Date: 1700000804,
	})
	if err != nil {
		t.Fatalf("send mention: %v", err)
	}
	mentionID := mention.Message.ID

	var dialogRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*)::int FROM channel_dialogs WHERE user_id = $1 AND channel_id = $2`, reader.ID, channelID).Scan(&dialogRows); err != nil {
		t.Fatalf("count reader dialog rows: %v", err)
	}
	if dialogRows != 1 {
		t.Fatalf("reader dialog rows = %d, want 1 before the race", dialogRows)
	}

	// L：锁住发送者的 members 行，readHistory 推进 read_outbox 时会被它挡住。
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	defer func() { _ = lock.Rollback(context.Background()) }()
	if _, err := lock.Exec(ctx, `SELECT 1 FROM channel_members WHERE channel_id = $1 AND user_id = $2 FOR UPDATE`, channelID, sender.ID); err != nil {
		t.Fatalf("lock sender member row: %v", err)
	}

	waiting := func() int {
		var n int
		if err := pool.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM pg_locks l
JOIN pg_stat_activity a ON a.pid = l.pid
WHERE NOT l.granted AND a.datname = current_database()`).Scan(&n); err != nil {
			t.Fatalf("count waiting locks: %v", err)
		}
		return n
	}
	waitFor := func(want int, what string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for waiting() < want {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (waiting locks = %d, want >= %d)", what, waiting(), want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	historyErr := make(chan error, 1)
	go func() {
		_, err := channels.ReadChannelHistory(ctx, domain.ReadChannelHistoryRequest{
			UserID: reader.ID, ChannelID: channelID, MaxID: mentionID, Date: 1700000805,
		})
		historyErr <- err
	}()
	waitFor(1, "readHistory to block on the sender member row")

	contentsErr := make(chan error, 1)
	go func() {
		_, err := channels.readChannelMessageContentsOnce(ctx, domain.ReadChannelMessageContentsRequest{
			UserID: reader.ID, ChannelID: channelID, IDs: []int{mentionID},
		})
		contentsErr <- err
	}()
	waitFor(2, "readMessageContents to block behind readHistory")

	if err := lock.Commit(ctx); err != nil {
		t.Fatalf("commit lock tx: %v", err)
	}

	var failures []error
	for name, ch := range map[string]chan error{"readHistory": historyErr, "readMessageContents": contentsErr} {
		select {
		case err := <-ch:
			if err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "40P01" {
					err = fmt.Errorf("deadlock (40P01) %s: %w", pgErr.Detail, err)
				}
				failures = append(failures, fmt.Errorf("%s: %w", name, err))
			}
		case <-ctx.Done():
			failures = append(failures, fmt.Errorf("%s: timed out", name))
		}
	}
	if len(failures) > 0 {
		t.Fatalf("concurrent readMessageContents + readHistory failed: %v", failures)
	}

	var mentionUnread bool
	if err := pool.QueryRow(ctx, `SELECT unread FROM channel_unread_mentions WHERE user_id = $1 AND channel_id = $2 AND message_id = $3`, reader.ID, channelID, mentionID).Scan(&mentionUnread); err != nil {
		t.Fatalf("read mention row: %v", err)
	}
	if mentionUnread {
		t.Fatalf("mention %d still unread, want read", mentionID)
	}
	var mentionsCount, dialogReadInbox, memberReadInbox int
	if err := pool.QueryRow(ctx, `SELECT unread_mentions_count, read_inbox_max_id FROM channel_dialogs WHERE user_id = $1 AND channel_id = $2`, reader.ID, channelID).Scan(&mentionsCount, &dialogReadInbox); err != nil {
		t.Fatalf("read reader dialog: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT read_inbox_max_id FROM channel_members WHERE user_id = $1 AND channel_id = $2`, reader.ID, channelID).Scan(&memberReadInbox); err != nil {
		t.Fatalf("read reader member: %v", err)
	}
	if mentionsCount != 0 {
		t.Fatalf("dialog unread_mentions_count = %d, want 0", mentionsCount)
	}
	if dialogReadInbox != mentionID || memberReadInbox != mentionID {
		t.Fatalf("read_inbox dialog=%d member=%d, want both %d", dialogReadInbox, memberReadInbox, mentionID)
	}
}
