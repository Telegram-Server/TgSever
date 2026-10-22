package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/pgtest"
	"telesrv/internal/store/storetest"
)

// monoforumUsers 建群主与两个订阅者；结束时删掉群主建的频道（母频道与 monoforum）和三个用户。
func monoforumUsers(t *testing.T, pool *pgxpool.Pool, phonePrefix string) (owner, sub, other int64) {
	t.Helper()
	owner, sub, other = createMonoforumUsers(t, pool, phonePrefix)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE creator_user_id = $1", owner)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner, sub, other})
	})
	return owner, sub, other
}

// createMonoforumUsers 建群主与两个订阅者，不清理（给独占库用）。
func createMonoforumUsers(t *testing.T, pool *pgxpool.Pool, phonePrefix string) (owner, sub, other int64) {
	t.Helper()
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	ids := make([]int64, 0, 3)
	for i, name := range []string{"MonoOwner", "MonoSub", "MonoOther"} {
		u, err := users.Create(ctx, domain.User{AccessHash: int64(91 + i), Phone: fmt.Sprintf("%s%s%02d", phonePrefix, suffix, 41+i), FirstName: name})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		ids = append(ids, u.ID)
	}
	return ids[0], ids[1], ids[2]
}

// TestSendMonoforumMessageAndHistoryPostgres 跑频道私信(monoforum)发送+读历史的共享契约。
// 群主删除 monoforum 消息只能走私有的 deleteChannelMessagesTx（群主不是 monoforum 成员）。
func TestSendMonoforumMessageAndHistoryPostgres(t *testing.T) {
	pool := testPool(t)
	channels := NewChannelStore(pool)
	storetest.MonoforumSendAndHistory(t, func(t *testing.T) storetest.MonoforumFixture {
		owner, sub, other := monoforumUsers(t, pool, "+1779")
		return storetest.MonoforumFixture{
			Channels: channels, Dialogs: NewDialogStore(pool), Owner: owner, Sub: sub, Other: other,
			DeleteAsCreator: func(t *testing.T, monoID, creatorID int64, ids []int, date int) domain.ChannelUpdateEvent {
				t.Helper()
				ctx := context.Background()
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatalf("begin monoforum delete: %v", err)
				}
				defer func() { _ = tx.Rollback(ctx) }()
				mono, err := getChannelByID(ctx, tx, monoID)
				if err != nil {
					t.Fatalf("load monoforum for delete: %v", err)
				}
				member := domain.ChannelMember{ChannelID: monoID, UserID: creatorID, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive}
				_, event, _, err := channels.deleteChannelMessagesTx(ctx, tx, mono, member, ids, creatorID, date, domain.UserDeletionAudit(domain.DeletionSourceUser, creatorID, [8]byte{}, 0), false)
				if err != nil {
					t.Fatalf("delete monoforum message: %v", err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatalf("commit monoforum delete: %v", err)
				}
				return event
			},
		}
	})
}

// TestMonoforumSendStoragePostgres 只覆盖 Postgres 独有的存储细节：request_fingerprint 落库长度、
// 订阅者看不到的消息不写 viewer 行，以及 LookupChannelSendReplay 拒绝别的子会话的指纹。
func TestMonoforumSendStoragePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	owner, sub, other := monoforumUsers(t, pool, "+1778")
	channels := NewChannelStore(pool)
	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Mono storage", Broadcast: true, Date: 1700001000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channels.SetPaidMessagesPrice(ctx, owner, broadcast.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: sub}
	otherPeer := domain.Peer{Type: domain.PeerTypeUser, ID: other}
	send := func(req domain.SendMonoforumMessageRequest) domain.SendChannelMessageResult {
		t.Helper()
		req.MonoforumID = monoID
		res, err := channels.SendMonoforumMessage(ctx, req)
		if err != nil {
			t.Fatalf("send %q: %v", req.Message, err)
		}
		return res
	}

	m1 := send(domain.SendMonoforumMessageRequest{SenderUserID: sub, SavedPeer: subPeer, RandomID: 111, Message: "hi", Date: 1700001001})
	var fingerprint []byte
	if err := pool.QueryRow(ctx, `SELECT request_fingerprint FROM channel_messages WHERE channel_id=$1 AND id=$2`, monoID, m1.Message.ID).Scan(&fingerprint); err != nil {
		t.Fatalf("load monoforum fingerprint: %v", err)
	}
	if len(fingerprint) != 32 {
		t.Fatalf("monoforum fingerprint length = %d, want 32", len(fingerprint))
	}

	hidden := send(domain.SendMonoforumMessageRequest{SenderUserID: other, SavedPeer: otherPeer, RandomID: 201, Message: "other", Date: 1700001005})
	if _, err := channels.GetChannelMessageViews(ctx, domain.ChannelMessageViewsRequest{
		UserID: sub, ChannelID: monoID, IDs: []int{m1.Message.ID, hidden.Message.ID}, Increment: true, Date: 1700001006,
	}); err != nil {
		t.Fatalf("subscriber get monoforum message views: %v", err)
	}
	var hiddenViews int
	var hiddenViewer bool
	if err := pool.QueryRow(ctx, `
SELECT m.views_count,
       EXISTS (
           SELECT 1
           FROM channel_message_viewers v
           WHERE v.channel_id = m.channel_id
             AND v.message_id = m.id
             AND v.viewer_user_id = $3
       )
FROM channel_messages m
WHERE m.channel_id = $1 AND m.id = $2`, monoID, hidden.Message.ID, sub).Scan(&hiddenViews, &hiddenViewer); err != nil {
		t.Fatalf("load hidden monoforum view state: %v", err)
	}
	if hiddenViews != 0 || hiddenViewer {
		t.Fatalf("hidden monoforum view state = count %d viewer %v, want 0/false", hiddenViews, hiddenViewer)
	}

	// SavedPeer 既属于查找范围，也属于回退意图：跨子会话用同一 random_id 合法，但对已存在的
	// 范围出示另一个子会话的指纹必须被拒，而不是当作重放。
	send(domain.SendMonoforumMessageRequest{SenderUserID: owner, SavedPeer: subPeer, RandomID: 9001, Message: "to sub", Date: 1700001010})
	send(domain.SendMonoforumMessageRequest{SenderUserID: owner, SavedPeer: otherPeer, RandomID: 9001, Message: "to other", Date: 1700001011})
	otherIntentFingerprint, err := store.MonoforumSendFingerprint(domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: owner, SavedPeer: otherPeer, RandomID: 9001, Message: "to sub",
	})
	if err != nil {
		t.Fatalf("fingerprint mismatched saved peer: %v", err)
	}
	if _, _, err := channels.LookupChannelSendReplay(ctx, domain.ChannelSendReplayRequest{
		ChannelID: monoID, SenderUserID: owner, SavedPeer: subPeer, RandomID: 9001, IdempotencyFingerprint: otherIntentFingerprint,
	}); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("mismatched saved-peer fingerprint err = %v, want ErrMessageRandomIDDuplicate", err)
	}
}

// TestSendPaidMonoforumMessageLedgerPostgres 跑付费私信账本契约。用独占库且子测试之间不删频道：
// 频道余额表不随频道删除，删掉的频道 id 被复用时新频道会带着上一个的余额。库随测试一起删除。
func TestSendPaidMonoforumMessageLedgerPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := Open(ctx, pgtest.NewDatabase(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	channels := NewChannelStore(pool)
	stars := NewStarsStore(pool)
	storetest.MonoforumPaidMessages(t, func(t *testing.T) storetest.PaidMonoforumFixture {
		owner, sub, other := createMonoforumUsers(t, pool, "+1789")
		return storetest.PaidMonoforumFixture{
			Channels: channels, Owner: owner, Sub: sub, Other: other,
			GrantStars: func(t *testing.T, userID, amount int64) {
				t.Helper()
				if _, _, err := stars.EnsureGrant(ctx, userID, amount, 1700002000); err != nil {
					t.Fatalf("grant %d stars to %d: %v", amount, userID, err)
				}
			},
			StarsBalances: func(t *testing.T, userID, channelID int64) (int64, int64) {
				t.Helper()
				var user, channel int64
				if err := pool.QueryRow(ctx, `
SELECT COALESCE((SELECT balance FROM stars_balances WHERE user_id = $1), 0),
       COALESCE((SELECT balance FROM channel_stars_balances WHERE channel_id = $2), 0)`, userID, channelID).Scan(&user, &channel); err != nil {
					t.Fatalf("load balances: %v", err)
				}
				return user, channel
			},
		}
	})
}

// TestPaidMonoforumMessageStarsColumnPostgres 只断言 Postgres 把实际价格落进 paid_message_stars 列。
func TestPaidMonoforumMessageStarsColumnPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	owner, sub, _ := monoforumUsers(t, pool, "+1788")
	channels := NewChannelStore(pool)
	broadcast, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Paid column", Broadcast: true, Date: 1700002000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channels.SetPaidMessagesPrice(ctx, owner, broadcast.Channel.ID, 10, true)
	if err != nil {
		t.Fatalf("enable paid DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM channel_stars_balances WHERE channel_id = $1", broadcast.Channel.ID)
	})
	if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, sub, 25, 1700002000); err != nil {
		t.Fatalf("grant subscriber stars: %v", err)
	}
	paid, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID: monoID, SenderUserID: sub, SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: sub}, RandomID: 4002, Message: "paid", AllowPaidStars: 99, Date: 1700002002,
	})
	if err != nil {
		t.Fatalf("paid send: %v", err)
	}
	var persistedPaid int64
	if err := pool.QueryRow(ctx, `SELECT paid_message_stars FROM channel_messages WHERE channel_id=$1 AND id=$2`, monoID, paid.Message.ID).Scan(&persistedPaid); err != nil {
		t.Fatalf("load persisted paid stars: %v", err)
	}
	if persistedPaid != 10 {
		t.Fatalf("persisted paid_message_stars = %d, want 10", persistedPaid)
	}
}
