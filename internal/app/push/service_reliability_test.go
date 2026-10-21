package push

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func (f *serviceFixture) devicesOf(userID int64) []domain.PushDevice {
	f.t.Helper()
	got, err := f.devices.ListPushDevicesByUser(context.Background(), userID)
	if err != nil {
		f.t.Fatal(err)
	}
	return got
}

func (f *serviceFixture) sentCount() int {
	f.sender.mu.Lock()
	defer f.sender.mu.Unlock()
	return len(f.sender.sent)
}

func TestInvalidTokenDeletesRegistrationAndStopsPushing(t *testing.T) {
	f := newServiceFixture(t, Config{})
	f.sender.respond = func(n Notification) (Result, error) {
		if n.Token == "tok-dead" {
			return Result{StatusCode: 410, Reason: "Unregistered"}, nil
		}
		return Result{StatusCode: 200}, nil
	}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-dead", nil, presenceNone)
	f.register([8]byte{3}, bobID, domain.PushTokenAPNs, "tok-alive", nil, presenceNone)
	f.start()
	f.svc.NewMessage(textFromAlice("first"))
	f.stop()

	devices := f.devicesOf(bobID)
	if len(devices) != 1 || devices[0].Token != "tok-alive" {
		t.Fatalf("registrations after 410 = %+v, want only tok-alive", devices)
	}

	f.start()
	f.svc.NewMessage(textFromAlice("second"))
	f.stop()
	if got := f.sender.tokens(); len(got) != 3 || got[0] != "tok-alive" || got[1] != "tok-alive" || got[2] != "tok-dead" {
		t.Fatalf("pushed tokens = %v, want tok-dead once and tok-alive twice", got)
	}
}

func TestRegistrationOfAnotherUserNowIsNotPushedAndIsDeleted(t *testing.T) {
	f := newServiceFixture(t, Config{})
	const carolID = int64(1000003)
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-rebound", nil, presenceNone)
	f.register([8]byte{3}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	// 扫码登录接受时原地改写了授权的 user：登记里仍是 bob。
	if err := f.auths.Bind(context.Background(), domain.Authorization{AuthKeyID: [8]byte{2}, UserID: carolID, Hash: 2}); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.svc.NewMessage(textFromAlice("for bob only"))
	f.stop()

	if got := f.sender.tokens(); len(got) != 1 || got[0] != "tok-bob" {
		t.Fatalf("pushed tokens = %v, want only tok-bob", got)
	}
	devices := f.devicesOf(bobID)
	if len(devices) != 1 || devices[0].Token != "tok-bob" {
		t.Fatalf("bob's registrations = %+v, want the rebound one deleted", devices)
	}
}

type countingMetrics struct {
	mu      sync.Mutex
	dropped map[string]int
	// sentKinds 按发送顺序记下每次发送（含重试）的 kind 标签。
	sentKinds []string
}

func (m *countingMetrics) PushQueueDropped(kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dropped[kind]++
}

func (m *countingMetrics) PushSent(kind string, _ Result, _ error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sentKinds = append(m.sentKinds, kind)
}

func (m *countingMetrics) PushDeviceRemoved(string) {}

func TestFullQueueDropsAndCountsWithoutBlocking(t *testing.T) {
	auths := memory.NewAuthorizationStore()
	devices := memory.NewPushDeviceStore(auths)
	sender := newRecordingSender()
	sender.block = make(chan struct{})
	metrics := &countingMetrics{dropped: map[string]int{}}
	// 只留一个 worker（没有紧急专用 worker），队列容量 1：第二条排队，第三条起丢弃。
	svc := NewService(Config{QueueSize: 1, Workers: 1, UrgentWorkers: -1, SendTimeout: time.Hour}, Deps{
		Devices: devices, Authorizations: auths, Sender: sender, Metrics: metrics,
	}, zaptest.NewLogger(t))
	ctx := context.Background()
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: [8]byte{2}, UserID: bobID, Hash: 2}); err != nil {
		t.Fatal(err)
	}
	if err := devices.RegisterPushDevice(ctx, domain.PushDevice{AuthKeyID: [8]byte{2}, UserID: bobID, TokenType: domain.PushTokenAPNs, Token: "tok-bob"}); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(runCtx)
	}()
	defer func() {
		close(sender.block)
		cancel()
		<-done
	}()

	if !svc.NewMessage(textFromAlice("1")) {
		t.Fatal("first message rejected")
	}
	// 第一条卡在发送器里（worker 已取走），第二条占满容量为 1 的队列，第三条起丢弃。
	waitUntil(t, func() bool { return len(svc.urgent) == 0 })
	if !svc.NewMessage(textFromAlice("2")) {
		t.Fatal("second message rejected, want queued")
	}
	start := time.Now()
	for range 10 {
		if svc.NewMessage(textFromAlice("overflow")) {
			t.Fatal("message accepted into a full queue")
		}
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("NewMessage on a full queue took %v, want immediate", elapsed)
	}
	metrics.mu.Lock()
	dropped := metrics.dropped["message"]
	metrics.mu.Unlock()
	if dropped != 10 {
		t.Fatalf("dropped count = %d, want 10", dropped)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRetryableFailureIsRetriedConfiguredTimesThenGivenUp(t *testing.T) {
	f := newServiceFixture(t, Config{MaxRetries: 2, RetryBackoff: time.Millisecond})
	f.sender.respond = func(Notification) (Result, error) {
		return Result{StatusCode: 503, Reason: "ServiceUnavailable"}, nil
	}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()
	f.svc.NewMessage(textFromAlice("hello"))
	// 关停时不再重试：等重试发完再停。
	waitUntil(t, func() bool { return f.sentCount() == 3 })
	time.Sleep(20 * time.Millisecond)
	f.stop()

	// 首次发送 + 2 次重试，之后放弃；登记保留。
	if got := f.sentCount(); got != 3 {
		t.Fatalf("send attempts = %d, want 3", got)
	}
	if devices := f.devicesOf(bobID); len(devices) != 1 {
		t.Fatalf("registrations = %+v, want kept after transient failures", devices)
	}
}

func TestRetryStopsOnceTheGatewayAccepts(t *testing.T) {
	f := newServiceFixture(t, Config{MaxRetries: 5, RetryBackoff: time.Millisecond})
	attempts := 0
	f.sender.respond = func(Notification) (Result, error) {
		attempts++
		switch attempts {
		case 1:
			return Result{StatusCode: 429, Reason: "TooManyRequests"}, nil
		case 2:
			return Result{}, context.DeadlineExceeded
		default:
			return Result{StatusCode: 200}, nil
		}
	}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()
	f.svc.NewMessage(textFromAlice("hello"))
	waitUntil(t, func() bool { return f.sentCount() == 3 })
	time.Sleep(20 * time.Millisecond)
	f.stop()
	if got := f.sentCount(); got != 3 {
		t.Fatalf("send attempts = %d, want 3 (429, timeout, accepted)", got)
	}
}

func TestPermanentFailureIsNotRetried(t *testing.T) {
	f := newServiceFixture(t, Config{MaxRetries: 3, RetryBackoff: time.Millisecond})
	f.sender.respond = func(Notification) (Result, error) {
		return Result{StatusCode: 400, Reason: "BadTopic"}, nil
	}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()
	f.svc.NewMessage(textFromAlice("hello"))
	f.stop()
	if got := f.sentCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1", got)
	}
	if devices := f.devicesOf(bobID); len(devices) != 1 {
		t.Fatalf("registrations = %+v, want kept after a permanent failure", devices)
	}
}

func TestTopicMismatchKeepsRegistration(t *testing.T) {
	for _, reason := range []string{"DeviceTokenNotForTopic", "TopicDisallowed"} {
		t.Run(reason, func(t *testing.T) {
			f := newServiceFixture(t, Config{MaxRetries: 3, RetryBackoff: time.Millisecond})
			f.sender.respond = func(Notification) (Result, error) {
				return Result{StatusCode: 400, Reason: reason}, nil
			}
			f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
			f.start()
			f.svc.NewMessage(textFromAlice("hello"))
			f.svc.NewMessage(textFromAlice("again"))
			f.stop()
			if got := f.sentCount(); got != 2 {
				t.Fatalf("send attempts = %d, want 2 (one per message, no retry)", got)
			}
			if devices := f.devicesOf(bobID); len(devices) != 1 {
				t.Fatalf("registrations = %+v, want kept: %s is a server misconfiguration", devices, reason)
			}
		})
	}
}
