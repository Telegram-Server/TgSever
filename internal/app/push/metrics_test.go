package push

import (
	"context"
	"errors"
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

func TestSendResultLabel(t *testing.T) {
	cases := []struct {
		name   string
		result Result
		err    error
		want   string
	}{
		{"accepted", Result{StatusCode: 200}, nil, "sent"},
		{"gone", Result{StatusCode: 410, Reason: "Unregistered"}, nil, "invalid_token"},
		{"gone without reason", Result{StatusCode: 410}, nil, "invalid_token"},
		{"bad token", Result{StatusCode: 400, Reason: "BadDeviceToken"}, nil, "invalid_token"},
		{"wrong topic", Result{StatusCode: 400, Reason: "DeviceTokenNotForTopic"}, nil, "invalid_token"},
		{"throttled", Result{StatusCode: 429, Reason: "TooManyRequests"}, nil, "retryable"},
		{"apple down", Result{StatusCode: 503, Reason: "ServiceUnavailable"}, nil, "retryable"},
		{"timeout", Result{}, context.DeadlineExceeded, "retryable"},
		{"network", Result{}, errors.New("connection reset"), "retryable"},
		{"bad payload", Result{StatusCode: 400, Reason: "PayloadTooLarge"}, nil, "failed"},
		{"bad provider token", Result{StatusCode: 403, Reason: "InvalidProviderToken"}, nil, "failed"},
	}
	for _, tc := range cases {
		if got := SendResultLabel(tc.result, tc.err); got != tc.want {
			t.Errorf("%s: SendResultLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
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
	if svc.QueueCapacity() != 2 || svc.QueueLength() != 0 {
		t.Fatalf("queue = %d/%d, want 0/2", svc.QueueLength(), svc.QueueCapacity())
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
	SinkMetrics(sink).PushDeviceRemoved("invalid_token")
	if _, _, removed := sink.snapshot(); !slices.Equal(removed, []string{"invalid_token"}) {
		t.Fatalf("removed = %v", removed)
	}
}
