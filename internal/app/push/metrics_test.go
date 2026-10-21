package push

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type fakeSink struct {
	mu      sync.Mutex
	sent    []string
	dropped []string
	removed []string
}

func (s *fakeSink) PushSendResult(kind, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, kind+"/"+result)
}

func (s *fakeSink) PushQueueDropped(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropped = append(s.dropped, kind)
}

func (s *fakeSink) PushDeviceRemoved(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removed = append(s.removed, reason)
}

func (s *fakeSink) snapshot() (sent, dropped, removed []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent), slices.Clone(s.dropped), slices.Clone(s.removed)
}

// 指标经由 SinkMetrics 落到注册表：发送结果按类型与分类计数、队列满的丢弃计数，
// 队列长度与容量可在抓取时读到。
func TestServiceReportsMetricsThroughSink(t *testing.T) {
	auths := memory.NewAuthorizationStore()
	devices := memory.NewPushDeviceStore(auths)
	ctx := context.Background()
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: [8]byte{2}, UserID: bobID, Hash: 2}); err != nil {
		t.Fatal(err)
	}
	if err := devices.RegisterPushDevice(ctx, domain.PushDevice{AuthKeyID: [8]byte{2}, UserID: bobID, TokenType: domain.PushTokenAPNs, Token: "tok-bob"}); err != nil {
		t.Fatal(err)
	}
	sender := newRecordingSender()
	sender.block = make(chan struct{})
	sink := &fakeSink{}
	svc := NewService(Config{QueueSize: 2, Workers: 1, SendTimeout: time.Hour}, Deps{
		Devices: devices,
		Sender:  sender,
		Strings: testLangPack(),
		Metrics: SinkMetrics(sink),
	}, zaptest.NewLogger(t))
	// 紧急、批量两条 lane 各 QueueSize。
	if svc.QueueCapacity() != 4 || svc.QueueLength() != 0 {
		t.Fatalf("queue = %d/%d, want 0/4", svc.QueueLength(), svc.QueueCapacity())
	}
	// worker 未启动：两条进队列，第三条被丢弃。
	for range 3 {
		svc.NewMessage(textFromAlice("x"))
	}
	if svc.QueueLength() != 2 {
		t.Fatalf("QueueLength = %d, want 2", svc.QueueLength())
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(runCtx)
	}()
	close(sender.block)
	cancel()
	<-done
	sent, dropped, _ := sink.snapshot()
	if !slices.Equal(dropped, []string{"message"}) {
		t.Fatalf("dropped = %v, want one message drop", dropped)
	}
	if !slices.Equal(sent, []string{"message/sent", "message/sent"}) {
		t.Fatalf("sent = %v, want two message/sent", sent)
	}
}

// 失效清理计入指标：token 失效与授权换了用户各记一次，原因分开。
func TestServiceCountsRemovedRegistrations(t *testing.T) {
	auths := memory.NewAuthorizationStore()
	devices := memory.NewPushDeviceStore(auths)
	ctx := context.Background()
	for _, d := range []struct {
		key   [8]byte
		token string
	}{{[8]byte{2}, "tok-dead"}, {[8]byte{3}, "tok-moved"}} {
		if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: d.key, UserID: bobID, Hash: int64(d.key[0])}); err != nil {
			t.Fatal(err)
		}
		if err := devices.RegisterPushDevice(ctx, domain.PushDevice{AuthKeyID: d.key, UserID: bobID, TokenType: domain.PushTokenAPNs, Token: d.token}); err != nil {
			t.Fatal(err)
		}
	}
	// 扫码登录把 [3] 这个授权改写给了别的用户：登记的 user_id 不再一致。
	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: [8]byte{3}, UserID: aliceID, Hash: 3}); err != nil {
		t.Fatal(err)
	}
	sender := newRecordingSender()
	sender.respond = func(Notification) (Result, error) { return Result{StatusCode: 410, Reason: "Unregistered"}, nil }
	sink := &fakeSink{}
	svc := NewService(Config{}, Deps{
		Devices:        devices,
		Authorizations: auths,
		Sender:         sender,
		Strings:        testLangPack(),
		Metrics:        SinkMetrics(sink),
	}, zaptest.NewLogger(t))
	svc.NewMessage(textFromAlice("x"))
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.Run(runCtx)
	}()
	cancel()
	<-done
	sent, _, removed := sink.snapshot()
	slices.Sort(removed)
	if !slices.Equal(removed, []string{"invalid_token", "user_mismatch"}) {
		t.Fatalf("removed = %v, want invalid_token and user_mismatch", removed)
	}
	if !slices.Equal(sent, []string{"message/invalid_token"}) {
		t.Fatalf("sent = %v, want one message/invalid_token", sent)
	}
}
