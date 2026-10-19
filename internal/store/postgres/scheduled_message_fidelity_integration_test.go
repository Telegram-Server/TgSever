package postgres

import (
	"context"
	"reflect"
	"testing"

	"telesrv/internal/domain"
)

// TestScheduledMessageFidelityRoundTrip 验证定时行保存到点发出所需的全部元数据：回复引用
// （含只指向 forum 话题的回复）、send_as、完整转发头（channel_post、saved_from）在建、取、
// 到期认领各路径原样往返。门控于 TELESRV_TEST_POSTGRES_DSN。
func TestScheduledMessageFidelityRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 181, Phone: "+1783" + suffix + "01", FirstName: "FidelityOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	peerUser, err := users.Create(ctx, domain.User{AccessHash: 182, Phone: "+1783" + suffix + "02", FirstName: "FidelityPeer"})
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
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 1999000000 + int64(len(suffix))}
	reply := &domain.MessageReply{
		MessageID:     41,
		Peer:          peer,
		TopMessageID:  40,
		QuoteText:     "quoted",
		QuoteEntities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 6}},
		QuoteOffset:   3,
	}
	forward := &domain.MessageForward{
		From:           channelPeer,
		Date:           1699990000,
		ChannelPost:    77,
		SavedFrom:      peer,
		SavedFromMsgID: 55,
	}
	sendAs := &domain.Peer{Type: domain.PeerTypeChannel, ID: channelPeer.ID}
	full, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
		OwnerUserID:  owner.ID,
		Peer:         peer,
		RandomID:     8201,
		Message:      "forwarded reply",
		ReplyTo:      reply,
		Forward:      forward,
		SendAs:       sendAs,
		GroupedID:    9988,
		ScheduleDate: 1700003600,
		Date:         1700000000,
	})
	if err != nil {
		t.Fatalf("create scheduled: %v", err)
	}
	topicOnly := &domain.MessageReply{Peer: peer, TopMessageID: 12, ForumTopic: true}
	topic, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
		OwnerUserID:  owner.ID,
		Peer:         peer,
		RandomID:     8202,
		Message:      "into topic",
		ReplyTo:      topicOnly,
		ScheduleDate: 1700003600,
		Date:         1700000000,
	})
	if err != nil {
		t.Fatalf("create topic scheduled: %v", err)
	}

	assertFidelity := func(label string, got domain.ScheduledMessage) {
		t.Helper()
		switch got.RandomID {
		case 8201:
			if !reflect.DeepEqual(got.ReplyTo, reply) {
				t.Fatalf("%s reply = %+v, want %+v", label, got.ReplyTo, reply)
			}
			if !reflect.DeepEqual(got.Forward, forward) {
				t.Fatalf("%s forward = %+v, want %+v", label, got.Forward, forward)
			}
			if !reflect.DeepEqual(got.SendAs, sendAs) || got.GroupedID != 9988 {
				t.Fatalf("%s send_as/grouped = %+v/%d, want %+v/9988", label, got.SendAs, got.GroupedID, sendAs)
			}
		case 8202:
			if !reflect.DeepEqual(got.ReplyTo, topicOnly) {
				t.Fatalf("%s topic reply = %+v, want %+v", label, got.ReplyTo, topicOnly)
			}
		default:
			t.Fatalf("%s unexpected row %+v", label, got)
		}
	}
	assertFidelity("create", full)
	assertFidelity("create topic", topic)

	got, err := messages.GetScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner.ID, Peer: peer, IDs: []int{full.ID, topic.ID}})
	if err != nil {
		t.Fatalf("get scheduled: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("get scheduled = %+v, want 2 rows", got.Messages)
	}
	for _, msg := range got.Messages {
		assertFidelity("get", msg)
	}

	due, err := messages.ClaimDueScheduledMessages(ctx, 1700003600, 100, 60)
	if err != nil {
		t.Fatalf("claim due: %v", err)
	}
	claimed := 0
	for _, msg := range due {
		if msg.OwnerUserID != owner.ID {
			continue
		}
		claimed++
		assertFidelity("claim due", msg)
	}
	if claimed != 2 {
		t.Fatalf("claimed %d own rows, want 2", claimed)
	}
}
