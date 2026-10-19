package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestScheduledMessagesLifecycleUsesScheduledStore(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700001000)
	)
	alice := domain.User{ID: aliceID, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: bobID, AccessHash: 22, FirstName: "Bob"}
	bobPeer := domain.Peer{Type: domain.PeerTypeUser, ID: bobID}
	messages := newScheduledCaptureMessages()
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    mapUsersService{users: map[int64]domain.User{aliceID: alice, bobID: bob}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	ctx := WithUserID(context.Background(), aliceID)

	sendReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bobID, AccessHash: bob.AccessHash},
		Message:  "later",
		RandomID: 123456,
	}
	sendReq.SetScheduleDate(int(now + 3600))
	updates, err := r.onMessagesSendMessage(ctx, sendReq)
	if err != nil {
		t.Fatalf("send scheduled message: %v", err)
	}
	gotUpdates := updates.(*tg.Updates).Updates
	if len(gotUpdates) != 2 {
		t.Fatalf("scheduled send updates = %+v, want id + new scheduled", gotUpdates)
	}
	newScheduled, ok := gotUpdates[1].(*tg.UpdateNewScheduledMessage)
	if !ok {
		t.Fatalf("scheduled update = %T, want UpdateNewScheduledMessage", gotUpdates[1])
	}
	scheduledMsg, ok := newScheduled.Message.(*tg.Message)
	if !ok || !scheduledMsg.FromScheduled || scheduledMsg.Message != "later" || scheduledMsg.Date != int(now+3600) {
		t.Fatalf("scheduled message = %#v, want from_scheduled later at schedule date", newScheduled.Message)
	}
	if row, ok := messages.scheduledRow(t, aliceID, bobPeer, scheduledMsg.ID); !ok || row.Message != "later" || row.ScheduleDate != int(now+3600) || row.RandomID != 123456 {
		t.Fatalf("stored scheduled row = %+v ok %v, want alice->bob at requested date", row, ok)
	}

	history, err := r.onMessagesGetScheduledHistory(ctx, &tg.MessagesGetScheduledHistoryRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: bob.AccessHash},
	})
	if err != nil {
		t.Fatalf("get scheduled history: %v", err)
	}
	if list := history.(*tg.MessagesMessages).Messages; len(list) != 1 {
		t.Fatalf("scheduled history = %+v, want one message", list)
	}

	editReq := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: bob.AccessHash},
		ID:   scheduledMsg.ID,
	}
	editReq.SetMessage("edited")
	editReq.SetScheduleDate(int(now + 7200))
	edited, err := r.onMessagesEditMessage(ctx, editReq)
	if err != nil {
		t.Fatalf("edit scheduled message: %v", err)
	}
	if row, ok := messages.scheduledRow(t, aliceID, bobPeer, scheduledMsg.ID); !ok || row.Message != "edited" || row.ScheduleDate != int(now+7200) {
		t.Fatalf("stored row after edit = %+v ok %v, want updated scheduled text/date", row, ok)
	}
	editedMessage := edited.(*tg.Updates).Updates[0].(*tg.UpdateNewScheduledMessage).Message.(*tg.Message)
	if !editedMessage.FromScheduled || editedMessage.Message != "edited" || editedMessage.Date != int(now+7200) {
		t.Fatalf("edited scheduled message = %#v, want edited future scheduled item", editedMessage)
	}

	sent, err := r.onMessagesSendScheduledMessages(ctx, &tg.MessagesSendScheduledMessagesRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: bob.AccessHash},
		ID:   []int{scheduledMsg.ID},
	})
	if err != nil {
		t.Fatalf("send scheduled now: %v", err)
	}
	sentID := messages.sendResult.SenderMessage.ID
	if messages.sendReq.Message != "edited" || sentID == 0 {
		t.Fatalf("sent scheduled request = %+v, want the edited text sent", messages.sendReq)
	}
	if row, ok := messages.scheduledRow(t, aliceID, bobPeer, scheduledMsg.ID); ok {
		t.Fatalf("scheduled row still queued after send: %+v", row)
	}
	var deleteUpdate *tg.UpdateDeleteScheduledMessages
	for _, update := range sent.(*tg.Updates).Updates {
		if v, ok := update.(*tg.UpdateDeleteScheduledMessages); ok {
			deleteUpdate = v
		}
	}
	if deleteUpdate == nil || len(deleteUpdate.Messages) != 1 || deleteUpdate.Messages[0] != scheduledMsg.ID {
		t.Fatalf("send scheduled delete update = %#v, want deleted scheduled id", deleteUpdate)
	}
	if sentIDs, ok := deleteUpdate.GetSentMessages(); !ok || len(sentIDs) != 1 || sentIDs[0] != sentID {
		t.Fatalf("delete scheduled sent ids = %+v ok %v, want sent message id %d", sentIDs, ok, sentID)
	}
}

// seedScheduledMedia 直接在定时队列里排一条带说明文字的文档消息。
func seedScheduledMedia(t *testing.T, messages *scheduledCaptureMessages, owner int64, peer domain.Peer, caption string, docID int64, scheduleDate int) (domain.ScheduledMessage, *domain.MessageMedia) {
	t.Helper()
	media := &domain.MessageMedia{
		Kind:     domain.MessageMediaKindDocument,
		Document: &domain.Document{ID: docID, AccessHash: 91, DCID: 2},
	}
	row, err := messages.ScheduleMessage(context.Background(), owner, domain.ScheduleMessageRequest{
		Peer: peer, Message: caption, Media: media, ScheduleDate: scheduleDate,
	})
	if err != nil {
		t.Fatalf("seed scheduled media: %v", err)
	}
	return row, media
}

func TestScheduledMessageEditDateOnlyPreservesContent(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700003000)
	)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: bobID}
	messages := newScheduledCaptureMessages()
	seeded, media := seedScheduledMedia(t, messages, aliceID, peer, "keep me", 910000000000000001, int(now+3600))
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	ctx := WithUserID(context.Background(), aliceID)

	editReq := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: 22},
		ID:   seeded.ID,
	}
	editReq.SetScheduleDate(int(now + 7200))
	updates, err := r.onMessagesEditMessage(ctx, editReq)
	if err != nil {
		t.Fatalf("date-only scheduled edit: %v", err)
	}
	row, ok := messages.scheduledRow(t, aliceID, peer, seeded.ID)
	if !ok || row.Message != "keep me" || row.Media == nil || row.Media.Document == nil || row.Media.Document.ID != media.Document.ID || row.ScheduleDate != int(now+7200) {
		t.Fatalf("scheduled item after date-only edit = %+v, want original caption/media and new date", row)
	}
	editedMessage := updates.(*tg.Updates).Updates[0].(*tg.UpdateNewScheduledMessage).Message.(*tg.Message)
	if !editedMessage.FromScheduled || editedMessage.Date != int(now+7200) || editedMessage.Message != "keep me" {
		t.Fatalf("date-only edit update = %#v, want scheduled message with its caption at new date", editedMessage)
	}
}

func TestScheduledMessageEditAllowsEmptyCaptionForMedia(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700004000)
	)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: bobID}
	messages := newScheduledCaptureMessages()
	seeded, media := seedScheduledMedia(t, messages, aliceID, peer, "caption", 910000000000000002, int(now+3600))
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	ctx := WithUserID(context.Background(), aliceID)

	editReq := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: 22},
		ID:   seeded.ID,
	}
	editReq.SetMessage("")
	editReq.SetScheduleDate(int(now + 7200))
	if _, err := r.onMessagesEditMessage(ctx, editReq); err != nil {
		t.Fatalf("empty-caption scheduled edit: %v", err)
	}
	row, ok := messages.scheduledRow(t, aliceID, peer, seeded.ID)
	if !ok || row.Message != "" || row.Media == nil || row.Media.Document == nil || row.Media.Document.ID != media.Document.ID || row.ScheduleDate != int(now+7200) {
		t.Fatalf("scheduled item after empty-caption edit = %+v, want media kept and caption cleared", row)
	}
}

func TestMessagesSetHistoryTTLPushesPrivatePeerBothSides(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700002000)
		period  = 86400
	)
	messages := &ttlCaptureMessages{captureMessages: &captureMessages{}}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Messages: messages, Sessions: sessions}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	ctx := WithSessionID(WithUserID(context.Background(), aliceID), 77)

	out, err := r.onMessagesSetHistoryTTL(ctx, &tg.MessagesSetHistoryTTLRequest{
		Peer:   &tg.InputPeerUser{UserID: bobID, AccessHash: 22},
		Period: period,
	})
	if err != nil {
		t.Fatalf("set private history ttl: %v", err)
	}
	if messages.setTTLUserID != aliceID || messages.setTTLPeer != (domain.Peer{Type: domain.PeerTypeUser, ID: bobID}) || messages.setTTLPeriod != period {
		t.Fatalf("set ttl context user=%d peer=%+v period=%d", messages.setTTLUserID, messages.setTTLPeer, messages.setTTLPeriod)
	}
	selfUpdate := out.(*tg.Updates).Updates[0].(*tg.UpdatePeerHistoryTTL)
	if peer, ok := selfUpdate.Peer.(*tg.PeerUser); !ok || peer.UserID != bobID {
		t.Fatalf("self ttl peer = %#v, want bob", selfUpdate.Peer)
	}
	if ttl, ok := selfUpdate.GetTTLPeriod(); !ok || ttl != period {
		t.Fatalf("self ttl period = %d ok %v, want %d", ttl, ok, period)
	}
	if got := sessions.pushedUserIDs(); len(got) != 2 || got[0] != aliceID || got[1] != bobID {
		t.Fatalf("pushed users = %+v, want alice then bob", got)
	}
	peerUpdates := sessions.snapshot().message.(*tg.Updates)
	peerUpdate := peerUpdates.Updates[0].(*tg.UpdatePeerHistoryTTL)
	if peer, ok := peerUpdate.Peer.(*tg.PeerUser); !ok || peer.UserID != aliceID {
		t.Fatalf("peer ttl update peer = %#v, want alice", peerUpdate.Peer)
	}
}
