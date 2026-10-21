package push

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// presenceState 模拟某个授权在 presence 里的状态。
type presenceState int

const (
	presenceNone       presenceState = iota // 没有会话记录（未连接或已断开）
	presenceForeground                      // 有连接且最近一次 updateStatus(offline=false)
	presenceBackground                      // 有连接但已 updateStatus(offline=true)
)

type fakePresence struct {
	mu     sync.Mutex
	states map[[8]byte]presenceState
}

func (f *fakePresence) AuthorizationInForeground(authKeyID [8]byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[authKeyID] == presenceForeground
}

type fakeUsers map[int64]domain.User

func (f fakeUsers) ByID(_ context.Context, _ int64, userID int64) (domain.User, bool, error) {
	u, ok := f[userID]
	return u, ok, nil
}

// recordingSender 记录收到的推送；block 非 nil 时每次发送都阻塞到 block 关闭或 ctx 结束。
type recordingSender struct {
	mu    sync.Mutex
	sent  []Notification
	block chan struct{}
	got   chan Notification
}

func newRecordingSender() *recordingSender {
	return &recordingSender{got: make(chan Notification, 64)}
}

func (s *recordingSender) Send(ctx context.Context, n Notification) (Result, error) {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	s.mu.Lock()
	s.sent = append(s.sent, n)
	s.mu.Unlock()
	select {
	case s.got <- n:
	default:
	}
	return Result{StatusCode: 200}, nil
}

func (s *recordingSender) tokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.sent))
	for _, n := range s.sent {
		out = append(out, n.Token)
	}
	sort.Strings(out)
	return out
}

type serviceFixture struct {
	t        *testing.T
	auths    *memory.AuthorizationStore
	devices  *memory.PushDeviceStore
	presence *fakePresence
	sender   *recordingSender
	svc      *Service
	cancel   context.CancelFunc
	done     chan struct{}
}

const (
	aliceID = int64(1000001)
	bobID   = int64(1000002)
)

func newServiceFixture(t *testing.T, cfg Config) *serviceFixture {
	t.Helper()
	auths := memory.NewAuthorizationStore()
	f := &serviceFixture{
		t:        t,
		auths:    auths,
		devices:  memory.NewPushDeviceStore(auths),
		presence: &fakePresence{states: map[[8]byte]presenceState{}},
		sender:   newRecordingSender(),
	}
	f.svc = NewService(cfg, Deps{
		Devices:  f.devices,
		Presence: f.presence,
		Users:    fakeUsers{aliceID: {ID: aliceID, FirstName: "Alice", LastName: "A"}, bobID: {ID: bobID, FirstName: "Bob"}},
		Sender:   f.sender,
		Strings:  testLangPack(),
	}, zaptest.NewLogger(t))
	return f
}

func (f *serviceFixture) start() {
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	go func() {
		defer close(f.done)
		f.svc.Run(ctx)
	}()
	f.t.Cleanup(f.stop)
}

// stop 停止 worker，并等它们处理完已入队的任务。
func (f *serviceFixture) stop() {
	if f.cancel == nil {
		return
	}
	f.cancel()
	<-f.done
	f.cancel = nil
}

func (f *serviceFixture) register(authKeyID [8]byte, userID int64, tokenType int, token string, secret []byte, state presenceState) {
	f.t.Helper()
	ctx := context.Background()
	if err := f.auths.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: userID, Hash: int64(authKeyID[0])}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.devices.RegisterPushDevice(ctx, domain.PushDevice{AuthKeyID: authKeyID, UserID: userID, TokenType: tokenType, Token: token, Secret: secret}); err != nil {
		f.t.Fatal(err)
	}
	f.presence.states[authKeyID] = state
}

func textFromAlice(text string) NewMessage {
	return NewMessage{
		Chat:       Chat{Kind: ChatPrivate, ID: aliceID},
		SenderID:   aliceID,
		Recipients: []int64{bobID},
		MessageID:  77,
		Text:       text,
	}
}

func TestNewMessagePushesEveryDeviceNotInForeground(t *testing.T) {
	f := newServiceFixture(t, Config{})
	// 同一用户：前台设备不推；有连接但已报后台的推；没有连接的推；
	// 另一台设备在前台不影响其他设备。
	f.register([8]byte{1}, bobID, domain.PushTokenAPNs, "tok-foreground", testSecret(), presenceForeground)
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-background", testSecret(), presenceBackground)
	f.register([8]byte{3}, bobID, domain.PushTokenAPNs, "tok-offline", nil, presenceNone)
	// VoIP 登记只用于来电，不收新消息推送。
	f.register([8]byte{4}, bobID, domain.PushTokenVoIP, "tok-voip", testSecret(), presenceNone)
	// 发送者自己的设备不收这条推送。
	f.register([8]byte{5}, aliceID, domain.PushTokenAPNs, "tok-alice", nil, presenceNone)
	f.start()

	if !f.svc.NewMessage(textFromAlice("hello")) {
		t.Fatal("NewMessage rejected, want queued")
	}
	f.stop()

	got := f.sender.tokens()
	want := []string{"tok-background", "tok-offline"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("pushed tokens = %v, want %v", got, want)
	}
}

func TestNewMessagePushCarriesRenderedEncryptedContent(t *testing.T) {
	f := newServiceFixture(t, Config{})
	secret := testSecret()
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", secret, presenceNone)
	f.start()
	f.svc.NewMessage(textFromAlice("hello bob"))
	f.stop()

	if len(f.sender.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(f.sender.sent))
	}
	n := f.sender.sent[0]
	if n.Token != "tok-bob" || n.Type != PushTypeAlert || n.Priority != PriorityHigh {
		t.Fatalf("notification = %+v, want alert to tok-bob with priority 10", n)
	}
	var outer struct {
		P string `json:"p"`
	}
	if err := json.Unmarshal(n.Payload, &outer); err != nil || outer.P == "" {
		t.Fatalf("payload %s: want encrypted p (err %v)", n.Payload, err)
	}
	plain, _, err := nseDecrypt(secret, outer.P)
	if err != nil {
		t.Fatal(err)
	}
	inner := decodeJSONObject(t, plain)
	alert := inner["aps"].(map[string]any)["alert"].(map[string]any)
	if inner["from_id"] != "1000001" || inner["msg_id"] != "77" || alert["title"] != "Alice A" || alert["body"] != "hello bob" {
		t.Fatalf("decrypted payload = %s, want Alice's text", plain)
	}
}

func TestNewMessageSkipsSecretOfWrongLength(t *testing.T) {
	f := newServiceFixture(t, Config{})
	// 带了 secret 但长度不对：不能退回明文（会把内容暴露给 Apple），这台设备不推。
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bad-secret", make([]byte, 16), presenceNone)
	f.start()
	f.svc.NewMessage(textFromAlice("secret text"))
	f.stop()
	if got := f.sender.tokens(); len(got) != 0 {
		t.Fatalf("pushed = %v, want none", got)
	}
}

func TestNewMessageDoesNotBlockWhenSenderIsStuck(t *testing.T) {
	f := newServiceFixture(t, Config{QueueSize: 2, Workers: 1, SendTimeout: time.Hour})
	f.sender.block = make(chan struct{})
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()

	done := make(chan int)
	go func() {
		accepted := 0
		for range 50 {
			if f.svc.NewMessage(textFromAlice("x")) {
				accepted++
			}
		}
		done <- accepted
	}()
	select {
	case accepted := <-done:
		// 1 条卡在发送器里，队列再收 2 条，其余丢弃。
		if accepted > 3 || accepted < 1 {
			t.Fatalf("accepted = %d, want the bounded queue to drop the overflow", accepted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NewMessage blocked behind a stuck sender")
	}
	close(f.sender.block)
}

func TestNewMessageWithoutSenderIsDisabled(t *testing.T) {
	svc := NewService(Config{}, Deps{}, zaptest.NewLogger(t))
	if svc.NewMessage(textFromAlice("x")) {
		t.Fatal("NewMessage accepted with no sender, want disabled")
	}
}
