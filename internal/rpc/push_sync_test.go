package rpc

import (
	"bytes"
	"context"
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appauth "telesrv/internal/app/auth"
	appdialogs "telesrv/internal/app/dialogs"
	appmessages "telesrv/internal/app/messages"
	"telesrv/internal/app/push"
	appupdates "telesrv/internal/app/updates"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/postgres"
	"telesrv/internal/store/postgres/pgtest"
)

// pushSyncFixture 是已读/删除同步的接线环境：Bob 有一台桌面端（desktopKey）和一台登记了
// 推送、已切到后台的 iPhone（phoneKey）；消息、已读、删除都走真实的内存 store 与 outbox。
type pushSyncFixture struct {
	t          *testing.T
	ctx        context.Context
	router     *Router
	messages   store.MessageStore
	dispatcher *OutboxDispatcher
	sender     *capturePushSender
	svc        *push.Service
	alice, bob domain.User
	stop       func()
}

var (
	pushSyncDesktopKey = [8]byte{0x10, 0x01}
	pushSyncPhoneKey   = [8]byte{0x10, 0x02}
	pushSyncAliceKey   = [8]byte{0x10, 0x03}
)

const pushSyncToken = "a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1"

// pushSyncStores 是接线测试用到的一组存储；内存版与 Postgres 版各一份。
type pushSyncStores struct {
	authKeys       store.AuthKeyStore
	authorizations store.AuthorizationStore
	devices        store.PushDeviceStore
	users          store.UserStore
	dialogs        store.DialogStore
	messages       store.MessageStore
	events         store.UpdateEventStore
	states         store.UpdateStateStore
	outbox         store.DispatchOutboxStore
}

func memoryPushSyncStores(*testing.T) pushSyncStores {
	authKeys := memory.NewAuthKeyStore()
	authorizations := memory.NewAuthorizationStore()
	authorizations.LinkAuthKeyAuthority(authKeys)
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	events := memory.NewUpdateEventStore()
	messages.AttachUpdateEventStore(events)
	return pushSyncStores{
		authKeys: authKeys, authorizations: authorizations, devices: memory.NewPushDeviceStore(authorizations),
		users: memory.NewUserStore(), dialogs: dialogs, messages: messages, events: events,
		states: memory.NewUpdateStateStore(), outbox: memory.NewDispatchOutboxStore(events),
	}
}

// postgresPushSyncStores 用独立的库：outbox 按全库领取任务，不能和同包其他测试共用。
// 私聊已读只有 Postgres 实现会写 outbox（内存版只做在线推送），已读接线必须走这里。
func postgresPushSyncStores(t *testing.T) pushSyncStores {
	t.Helper()
	pool, err := postgres.Open(context.Background(), pgtest.NewDatabase(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pushSyncStores{
		authKeys: postgres.NewAuthKeyStore(pool), authorizations: postgres.NewAuthorizationStore(pool), devices: postgres.NewPushDeviceStore(pool),
		users: postgres.NewUserStore(pool), dialogs: postgres.NewDialogStore(pool), messages: postgres.NewMessageStore(pool),
		events: postgres.NewUpdateEventStore(pool), states: postgres.NewUpdateStateStore(pool), outbox: postgres.NewDispatchOutboxStore(pool),
	}
}

// forEachPushSyncBackend 在内存与 Postgres（设置了 TELESRV_TEST_POSTGRES_DSN 时）两种存储上各跑一遍。
func forEachPushSyncBackend(t *testing.T, run func(t *testing.T, stores pushSyncStores)) {
	t.Run("memory", func(t *testing.T) { run(t, memoryPushSyncStores(t)) })
	t.Run("postgres", func(t *testing.T) { run(t, postgresPushSyncStores(t)) })
}

func newPushSyncFixture(t *testing.T, stores pushSyncStores) *pushSyncFixture {
	t.Helper()
	ctx := context.Background()
	authKeys, authorizations, devices := stores.authKeys, stores.authorizations, stores.devices
	userStore, dialogStore, messageStore, events := stores.users, stores.dialogs, stores.messages, stores.events
	bob, err := userStore.Create(ctx, domain.User{Phone: "15550010001", FirstName: "Bob"})
	if err != nil {
		t.Fatal(err)
	}
	alice, err := userStore.Create(ctx, domain.User{Phone: "15550010002", FirstName: "Alice"})
	if err != nil {
		t.Fatal(err)
	}
	for i, binding := range []struct {
		key    [8]byte
		userID int64
	}{{pushSyncDesktopKey, bob.ID}, {pushSyncPhoneKey, bob.ID}, {pushSyncAliceKey, alice.ID}} {
		if err := authKeys.Save(ctx, store.AuthKeyData{ID: binding.key}); err != nil {
			t.Fatal(err)
		}
		if err := authorizations.Bind(ctx, domain.Authorization{AuthKeyID: binding.key, UserID: binding.userID, Hash: int64(10001 + i)}); err != nil {
			t.Fatal(err)
		}
	}
	sessions := &captureSessions{}
	r := New(Config{PushNewDeviceEnabled: true}, Deps{
		Auth:        appauth.NewService(userStore, authorizations, nil, authKeys, nil, "12345"),
		Users:       appusers.NewService(userStore),
		Updates:     appupdates.NewService(stores.states, events),
		Dialogs:     appdialogs.NewService(dialogStore),
		Messages:    appmessages.NewService(messageStore, dialogStore),
		Sessions:    sessions,
		PushDevices: devices,
	}, zaptest.NewLogger(t), clock.System)
	sender := &capturePushSender{}
	svc := push.NewService(push.Config{}, push.Deps{
		Devices:  devices,
		Presence: r,
		Users:    staticUsersService{user: alice},
		Sender:   sender,
	}, zaptest.NewLogger(t))
	f := &pushSyncFixture{
		t: t, ctx: ctx, router: r, messages: messageStore, sender: sender, svc: svc, alice: alice, bob: bob,
		// outbox 用自己的会话桩：captureSessions 会记住最近一次推送的用户，与 router 共用会串号。
		dispatcher: newTestOutboxDispatcher(events, stores.outbox, &captureSessions{}, zaptest.NewLogger(t),
			WithOutboxDeliveryObserver(PushOutboxObserver(svc))),
	}
	r.SetPushDeleteNotifier(svc)
	// iPhone 登记推送并切到后台（连接还在，已上报 offline=true）；桌面端在前台。
	f.dispatch(pushSyncPhoneKey, 2, &tg.AccountRegisterDeviceRequest{TokenType: domain.PushTokenAPNs, Token: pushSyncToken, Secret: bytes.Repeat([]byte{9}, push.SecretSize)})
	f.dispatch(pushSyncPhoneKey, 2, &tg.AccountUpdateStatusRequest{Offline: true})
	f.dispatch(pushSyncDesktopKey, 1, &tg.AccountUpdateStatusRequest{Offline: false})
	return f
}

func (f *pushSyncFixture) dispatch(key [8]byte, sessionID int64, req bin.Encoder) any {
	f.t.Helper()
	var buf bin.Buffer
	if err := req.Encode(&buf); err != nil {
		f.t.Fatal(err)
	}
	res, err := f.router.Dispatch(f.ctx, key, sessionID, &buf)
	if err != nil {
		f.t.Fatalf("dispatch %T: %v", req, err)
	}
	if rpcErr, ok := res.(*tg.Error); ok {
		f.t.Fatalf("dispatch %T: %v", req, rpcErr)
	}
	return res
}

// sendFromAlice 让 Alice 给 Bob 发一条私聊，返回 Bob 视角的 msg_id。
func (f *pushSyncFixture) sendFromAlice(text string, randomID int64) int {
	f.t.Helper()
	res, err := f.messages.SendPrivateText(f.ctx, domain.SendPrivateTextRequest{
		SenderUserID: f.alice.ID, RecipientUserID: f.bob.ID, RandomID: randomID, Message: text, Date: 1700000000,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return res.RecipientMessage.ID
}

func (f *pushSyncFixture) run(fn func()) {
	runCtx, stop := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { defer close(done); f.svc.Run(runCtx) }()
	fn()
	f.dispatcher.DispatchOnce(f.ctx)
	stop()
	<-done
}

// syncPushes 返回优先级 5 的推送（已读/删除同步）；新消息推送是优先级 10。
func (f *pushSyncFixture) syncPushes() []push.Notification {
	f.sender.mu.Lock()
	defer f.sender.mu.Unlock()
	var out []push.Notification
	for _, n := range f.sender.sent {
		if n.Priority == push.PriorityLow {
			out = append(out, n)
		}
	}
	return out
}

func (f *pushSyncFixture) bobPeerOfAlice() *tg.InputPeerUser {
	f.t.Helper()
	u, found, err := f.router.deps.Users.ByID(f.ctx, f.bob.ID, f.alice.ID)
	if err != nil || !found {
		f.t.Fatalf("lookup alice: found=%v err=%v", found, err)
	}
	return &tg.InputPeerUser{UserID: u.ID, AccessHash: u.AccessHash}
}

type recordingReadNotifier struct{ reads []push.ReadHistory }

func (*recordingReadNotifier) NewMessage(push.NewMessage) bool { return true }
func (n *recordingReadNotifier) ReadHistory(r push.ReadHistory) bool {
	n.reads = append(n.reads, r)
	return true
}

func TestPushOutboxObserverForwardsInboxReadsOfPrivateChatsAndChannels(t *testing.T) {
	notifier := &recordingReadNotifier{}
	observe := PushOutboxObserver(notifier)
	const me = int64(10)
	deliver := func(eventType domain.UpdateEventType, peer domain.Peer, maxID int) {
		observe(context.Background(), OutboxDelivery{Item: store.DispatchOutboxItem{TargetUserID: me},
			Event: domain.UpdateEvent{UserID: me, Type: eventType, Peer: peer, MaxID: maxID}})
	}
	deliver(domain.UpdateEventReadHistoryInbox, domain.Peer{Type: domain.PeerTypeUser, ID: 20}, 5)
	deliver(domain.UpdateEventReadHistoryInbox, domain.Peer{Type: domain.PeerTypeChannel, ID: 30}, 9)
	// 对方读了我的消息（outbox 已读）、收藏夹、没有 max_id 的都不推。
	deliver(domain.UpdateEventReadHistoryOutbox, domain.Peer{Type: domain.PeerTypeUser, ID: 20}, 6)
	deliver(domain.UpdateEventReadHistoryInbox, domain.Peer{Type: domain.PeerTypeUser, ID: me}, 7)
	deliver(domain.UpdateEventReadHistoryInbox, domain.Peer{Type: domain.PeerTypeUser, ID: 20}, 0)

	want := []push.ReadHistory{
		{UserID: me, Chat: push.Chat{Kind: push.ChatPrivate, ID: 20}, MaxID: 5},
		{UserID: me, Chat: push.Chat{Kind: push.ChatChannel, ID: 30}, MaxID: 9},
	}
	if len(notifier.reads) != len(want) || notifier.reads[0] != want[0] || notifier.reads[1] != want[1] {
		t.Fatalf("reads = %+v, want %+v", notifier.reads, want)
	}
}

// 接线：Bob 在桌面端读掉 Alice 的私聊消息，后台的 iPhone 收到一条 READ_HISTORY 同步推送。
func TestReadOnAnotherSessionSendsReadHistoryPushPostgres(t *testing.T) {
	f := newPushSyncFixture(t, postgresPushSyncStores(t))
	var msgID int
	f.run(func() { msgID = f.sendFromAlice("hi bob", 1) })
	f.run(func() {
		f.dispatch(pushSyncDesktopKey, 1, &tg.MessagesReadHistoryRequest{Peer: f.bobPeerOfAlice(), MaxID: msgID})
	})

	got := f.syncPushes()
	if len(got) != 1 {
		t.Fatalf("sync pushes = %+v, want one READ_HISTORY", got)
	}
	if n := got[0]; n.Token != pushSyncToken || n.Type != push.PushTypeAlert || n.CollapseID != "read_u"+strconv.FormatInt(f.alice.ID, 10) {
		t.Fatalf("push = %+v, want READ_HISTORY for Alice's chat to the iPhone", n)
	}
}

// 接线：Bob 在桌面端删掉 Alice 的私聊消息，后台的 iPhone 收到一条带会话的 MESSAGE_DELETED。
func TestDeleteOnAnotherSessionSendsMessageDeletedPush(t *testing.T) {
	forEachPushSyncBackend(t, testDeleteOnAnotherSessionSendsMessageDeletedPush)
}

func testDeleteOnAnotherSessionSendsMessageDeletedPush(t *testing.T, stores pushSyncStores) {
	f := newPushSyncFixture(t, stores)
	var first, second int
	f.run(func() {
		first = f.sendFromAlice("one", 1)
		second = f.sendFromAlice("two", 2)
	})
	f.run(func() {
		f.dispatch(pushSyncDesktopKey, 1, &tg.MessagesDeleteMessagesRequest{ID: []int{first, second}})
	})

	got := f.syncPushes()
	want := "del_u" + strconv.FormatInt(f.alice.ID, 10) + "_" + strconv.Itoa(first) + "_" + strconv.Itoa(second)
	if len(got) != 1 || got[0].Token != pushSyncToken || got[0].CollapseID != want {
		t.Fatalf("sync pushes = %+v, want one MESSAGE_DELETED with collapse id %q", got, want)
	}
}

// 双向删除：Alice 撤回消息，Bob 的 iPhone 也收到 MESSAGE_DELETED（会话是 Alice）。
func TestRevokeByPeerSendsMessageDeletedPushToRecipient(t *testing.T) {
	forEachPushSyncBackend(t, testRevokeByPeerSendsMessageDeletedPushToRecipient)
}

func testRevokeByPeerSendsMessageDeletedPushToRecipient(t *testing.T, stores pushSyncStores) {
	f := newPushSyncFixture(t, stores)
	var aliceMsgID, bobMsgID int
	f.run(func() {
		res, err := f.messages.SendPrivateText(f.ctx, domain.SendPrivateTextRequest{
			SenderUserID: f.alice.ID, RecipientUserID: f.bob.ID, RandomID: 7, Message: "oops", Date: 1700000000,
		})
		if err != nil {
			t.Fatal(err)
		}
		aliceMsgID, bobMsgID = res.SenderMessage.ID, res.RecipientMessage.ID
	})
	f.run(func() {
		f.dispatch(pushSyncAliceKey, 3, &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{aliceMsgID}})
	})

	got := f.syncPushes()
	want := "del_u" + strconv.FormatInt(f.alice.ID, 10) + "_" + strconv.Itoa(bobMsgID) + "_" + strconv.Itoa(bobMsgID)
	if len(got) != 1 || got[0].Token != pushSyncToken || got[0].CollapseID != want {
		t.Fatalf("sync pushes = %+v, want one MESSAGE_DELETED for Bob's box id, collapse id %q", got, want)
	}
}
