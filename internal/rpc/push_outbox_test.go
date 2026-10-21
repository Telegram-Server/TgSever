package rpc

import (
	"bytes"
	"context"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appauth "telesrv/internal/app/auth"
	"telesrv/internal/app/push"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type capturePushSender struct {
	mu   sync.Mutex
	sent []push.Notification
}

func (s *capturePushSender) Send(_ context.Context, n push.Notification) (push.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, n)
	return push.Result{StatusCode: 200}, nil
}

// 接线：account.registerDevice 之后，对方发来的私聊文字消息经 outbox 投递，
// 推送服务按 presence 判定设备不在前台，假发送器收到一条发往登记 token 的推送。
func TestRegisteredDeviceReceivesPushForIncomingPrivateText(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{0x95, 0x01}
	authKeys := memory.NewAuthKeyStore()
	authorizations := memory.NewAuthorizationStore()
	authorizations.LinkAuthKeyAuthority(authKeys)
	devices := memory.NewPushDeviceStore(authorizations)
	users := memory.NewUserStore()
	bob, err := users.Create(ctx, domain.User{Phone: "15550009501", FirstName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	alice, err := users.Create(ctx, domain.User{Phone: "15550009502", FirstName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := authKeys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	if err := authorizations.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: bob.ID, Hash: 9501}); err != nil {
		t.Fatal(err)
	}
	r := New(Config{}, Deps{
		Auth:        appauth.NewService(users, authorizations, nil, authKeys, nil, "12345"),
		PushDevices: devices,
	}, zaptest.NewLogger(t), clock.System)
	dispatch := func(req bin.Encoder) {
		t.Helper()
		var buf bin.Buffer
		if err := req.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Dispatch(ctx, authKeyID, 1, &buf); err != nil {
			t.Fatalf("dispatch %T: %v", req, err)
		}
	}
	const token = "4f1c2a0b9e8d7c6b5a4f1c2a0b9e8d7c6b5a4f1c2a0b9e8d7c6b5a4f1c2a0b9e"
	dispatch(&tg.AccountRegisterDeviceRequest{TokenType: domain.PushTokenAPNs, Token: token, Secret: bytes.Repeat([]byte{7}, push.SecretSize)})
	// App 切到后台：连接还在，但已上报 offline=true。
	dispatch(&tg.AccountUpdateStatusRequest{Offline: true})

	sender := &capturePushSender{}
	svc := push.NewService(push.Config{}, push.Deps{
		Devices:  devices,
		Presence: r,
		Users:    staticUsersService{user: alice},
		Sender:   sender,
	}, zaptest.NewLogger(t))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(runCtx) }()

	msg := domain.Message{
		ID: 31, OwnerUserID: bob.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID},
		From: domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}, Date: 1700000300, Body: "hi bob", Pts: 4,
	}
	events := &captureUpdateEventStore{events: []domain.UpdateEvent{{UserID: bob.ID, Type: domain.UpdateEventNewMessage, Pts: msg.Pts, PtsCount: 1, Date: msg.Date, Message: msg}}}
	outbox := &captureDispatchOutbox{items: []store.DispatchOutboxItem{{ID: 1, TargetUserID: bob.ID, Pts: msg.Pts, EventType: domain.UpdateEventNewMessage}}}
	dispatcher := newTestOutboxDispatcher(events, outbox, &captureSessions{}, zaptest.NewLogger(t), WithOutboxDeliveryObserver(PushOutboxObserver(svc)))
	dispatcher.DispatchOnce(ctx)
	stop()
	<-done

	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d pushes, want 1", len(sender.sent))
	}
	if got := sender.sent[0]; got.Token != token || got.Type != push.PushTypeAlert {
		t.Fatalf("push = %+v, want an alert to the registered token", got)
	}
}

func TestPushOutboxObserverOnlyForwardsIncomingPrivateMessages(t *testing.T) {
	var got []push.NewMessage
	observe := PushOutboxObserver(pushNotifierFunc(func(m push.NewMessage) bool { got = append(got, m); return true }))
	const me, peer = int64(10), int64(20)
	incoming := domain.Message{ID: 5, OwnerUserID: me, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peer}, From: domain.Peer{Type: domain.PeerTypeUser, ID: peer}, Body: "x", Silent: true, GroupedID: 77}
	deliver := func(eventType domain.UpdateEventType, msg domain.Message) {
		observe(context.Background(), OutboxDelivery{Item: store.DispatchOutboxItem{TargetUserID: me}, Event: domain.UpdateEvent{UserID: me, Type: eventType, Message: msg}})
	}
	out := incoming
	out.Out = true
	deleted := incoming
	deleted.Deleted = true
	service := incoming
	service.Media = &domain.MessageMedia{Kind: domain.MessageMediaKindService}
	self := incoming
	self.Peer, self.From = domain.Peer{Type: domain.PeerTypeUser, ID: me}, domain.Peer{Type: domain.PeerTypeUser, ID: me}
	deliver(domain.UpdateEventNewMessage, out)
	deliver(domain.UpdateEventNewMessage, deleted)
	deliver(domain.UpdateEventNewMessage, service)
	deliver(domain.UpdateEventNewMessage, self)
	deliver(domain.UpdateEventEditMessage, incoming)
	deliver(domain.UpdateEventNewMessage, incoming)
	if len(got) != 1 {
		t.Fatalf("forwarded %d messages, want only the incoming one: %+v", len(got), got)
	}
	m := got[0]
	if m.Chat != (push.Chat{Kind: push.ChatPrivate, ID: peer}) || m.SenderID != peer || len(m.Recipients) != 1 || m.Recipients[0] != me ||
		m.MessageID != 5 || m.Text != "x" || !m.Silent || m.GroupedID != 77 {
		t.Fatalf("forwarded = %+v", m)
	}
}

type pushNotifierFunc func(push.NewMessage) bool

func (f pushNotifierFunc) NewMessage(m push.NewMessage) bool { return f(m) }

func (pushNotifierFunc) ReadHistory(push.ReadHistory) bool { return false }
