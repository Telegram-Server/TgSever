package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ContactSignUpNoticeFixture 是同一份底层数据上的服务消息写入与读取面。NewUser 建一个账号（bot=true
// 时建 bot）；DeleteAccount 走该实现的账号注销。
type ContactSignUpNoticeFixture struct {
	Notices       store.ContactSignUpNoticeStore
	Messages      store.MessageStore
	Dialogs       store.DialogStore
	Events        store.UpdateEventStore
	NewUser       func(t *testing.T, bot bool) domain.User
	DeleteAccount func(t *testing.T, userID int64)
}

// ContactSignUpNoticeContract 是 contactSignUp 服务消息的存储契约：只写 Owner 一侧、带会话与
// pts 事件、静音标记、每对只写一次、不合格的账号什么都不写。
func ContactSignUpNoticeContract(t *testing.T, newFixture func(t *testing.T) ContactSignUpNoticeFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("writes an incoming service message to the owner only", func(t *testing.T) {
		f := newFixture(t)
		owner, from := f.NewUser(t, false), f.NewUser(t, false)
		date := int(time.Now().Unix())

		msg, created, err := f.Notices.DeliverContactSignUpNotice(ctx, domain.ContactSignUpNotice{OwnerUserID: owner.ID, FromUserID: from.ID, Date: date})
		if err != nil || !created {
			t.Fatalf("deliver created=%v err=%v", created, err)
		}
		fromPeer := domain.Peer{Type: domain.PeerTypeUser, ID: from.ID}
		if msg.OwnerUserID != owner.ID || msg.Peer != fromPeer || msg.From != fromPeer || msg.Out || msg.Silent || msg.Date != date || msg.ID <= 0 || msg.Pts <= 0 {
			t.Fatalf("message = %+v, want an incoming message from %d in owner %d's box", msg, from.ID, owner.ID)
		}
		if !isContactSignUp(msg) || msg.Body != "" {
			t.Fatalf("message media = %+v body=%q, want a bare contact_sign_up service action", msg.Media, msg.Body)
		}

		stored, err := f.Messages.GetByIDs(ctx, owner.ID, []int{msg.ID})
		if err != nil || len(stored.Messages) != 1 || !isContactSignUp(stored.Messages[0]) || stored.Messages[0].Peer != fromPeer {
			t.Fatalf("stored message = %+v err=%v", stored.Messages, err)
		}
		dialog, found := findDialog(t, f, owner.ID, fromPeer)
		if !found || dialog.TopMessage != msg.ID || dialog.UnreadCount != 1 {
			t.Fatalf("owner dialog with from = %+v found=%v, want top=%d unread=1", dialog, found, msg.ID)
		}
		events, err := f.Events.ListAfter(ctx, owner.ID, msg.Pts-1, 10)
		if err != nil || len(events) != 1 || events[0].Pts != msg.Pts || events[0].Type != domain.UpdateEventNewMessage {
			t.Fatalf("owner events after %d = %+v err=%v, want the new message event", msg.Pts-1, events, err)
		}

		// From 一侧什么都没有：没有会话，也没有事件。
		if _, found := findDialog(t, f, from.ID, domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}); found {
			t.Fatal("from got a dialog with the owner")
		}
		if events, err := f.Events.ListAfter(ctx, from.ID, 0, 10); err != nil || len(events) != 0 {
			t.Fatalf("from events = %+v err=%v, want none", events, err)
		}
	})

	t.Run("silent notice is stored silent", func(t *testing.T) {
		f := newFixture(t)
		owner, from := f.NewUser(t, false), f.NewUser(t, false)
		msg, _, err := f.Notices.DeliverContactSignUpNotice(ctx, domain.ContactSignUpNotice{OwnerUserID: owner.ID, FromUserID: from.ID, Silent: true, Date: int(time.Now().Unix())})
		if err != nil || !msg.Silent {
			t.Fatalf("silent notice = %+v err=%v, want silent", msg, err)
		}
		stored, err := f.Messages.GetByIDs(ctx, owner.ID, []int{msg.ID})
		if err != nil || len(stored.Messages) != 1 || !stored.Messages[0].Silent {
			t.Fatalf("stored silent notice = %+v err=%v", stored.Messages, err)
		}
	})

	t.Run("a pair is written once and replays the first message", func(t *testing.T) {
		f := newFixture(t)
		owner, from := f.NewUser(t, false), f.NewUser(t, false)
		n := domain.ContactSignUpNotice{OwnerUserID: owner.ID, FromUserID: from.ID, Date: int(time.Now().Unix())}
		first, _, err := f.Notices.DeliverContactSignUpNotice(ctx, n)
		if err != nil {
			t.Fatal(err)
		}
		n.Date += 60
		again, created, err := f.Notices.DeliverContactSignUpNotice(ctx, n)
		if err != nil || created || again.ID != first.ID || again.Pts != first.Pts || again.Date != first.Date {
			t.Fatalf("second deliver = %+v created=%v err=%v, want the first message %+v replayed", again, created, err, first)
		}
		dialog, _ := findDialog(t, f, owner.ID, domain.Peer{Type: domain.PeerTypeUser, ID: from.ID})
		if dialog.UnreadCount != 1 {
			t.Fatalf("unread after replay = %d, want 1", dialog.UnreadCount)
		}
		if events, _ := f.Events.ListAfter(ctx, owner.ID, first.Pts, 10); len(events) != 0 {
			t.Fatalf("replay appended events %+v", events)
		}
	})

	t.Run("ineligible accounts are rejected without writing", func(t *testing.T) {
		f := newFixture(t)
		owner, from, gone, bot := f.NewUser(t, false), f.NewUser(t, false), f.NewUser(t, false), f.NewUser(t, true)
		f.DeleteAccount(t, gone.ID)
		const missing = int64(1) << 50
		date := int(time.Now().Unix())
		for name, n := range map[string]domain.ContactSignUpNotice{
			"missing owner": {OwnerUserID: missing, FromUserID: from.ID, Date: date},
			"missing from":  {OwnerUserID: owner.ID, FromUserID: missing, Date: date},
			"deleted owner": {OwnerUserID: gone.ID, FromUserID: from.ID, Date: date},
			"deleted from":  {OwnerUserID: owner.ID, FromUserID: gone.ID, Date: date},
			"bot owner":     {OwnerUserID: bot.ID, FromUserID: from.ID, Date: date},
		} {
			if _, _, err := f.Notices.DeliverContactSignUpNotice(ctx, n); !errors.Is(err, domain.ErrUserNotFound) {
				t.Fatalf("%s: err=%v, want ErrUserNotFound", name, err)
			}
		}
		if events, err := f.Events.ListAfter(ctx, owner.ID, 0, 10); err != nil || len(events) != 0 {
			t.Fatalf("owner events after rejected notices = %+v err=%v, want none", events, err)
		}
	})
}

func isContactSignUp(msg domain.Message) bool {
	return msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindService &&
		msg.Media.ServiceAction != nil && msg.Media.ServiceAction.Kind == domain.MessageServiceActionContactSignUp
}

func findDialog(t *testing.T, f ContactSignUpNoticeFixture, owner int64, peer domain.Peer) (domain.Dialog, bool) {
	t.Helper()
	list, err := f.Dialogs.ListByUser(context.Background(), owner, domain.DialogFilter{Limit: 100})
	if err != nil {
		t.Fatalf("list dialogs of %d: %v", owner, err)
	}
	for _, d := range list.Dialogs {
		if d.Peer == peer {
			return d, true
		}
	}
	return domain.Dialog{}, false
}
