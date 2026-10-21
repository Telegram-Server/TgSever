package push

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// gatedSender 让 token 以 "slow-" 开头的推送阻塞到 gate 关闭（或 ctx 结束），其余立即成功。
type gatedSender struct {
	*recordingSender
	gate chan struct{}
}

func (s gatedSender) Send(ctx context.Context, n Notification) (Result, error) {
	if strings.HasPrefix(n.Token, "slow-") {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	return s.recordingSender.Send(ctx, n)
}

func waitForToken(t *testing.T, s *recordingSender, token string, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case n := <-s.got:
			if n.Token == token {
				return
			}
		case <-deadline:
			t.Fatalf("push to %s not sent within %v", token, within)
		}
	}
}

// 群组扇出把所有通用 worker 占满（每台设备都卡在网关上）时，私聊和来电推送仍立即发出。
func TestUrgentPushIsNotHeldUpByChannelFanOut(t *testing.T) {
	f := newChannelFixture(t, Config{Workers: 2, MemberBatch: 1, SendTimeout: time.Hour})
	gate := make(chan struct{})
	defer close(gate)
	f.svc.sender = gatedSender{recordingSender: f.sender, gate: gate}
	var members []int64
	for i := range 6 {
		id := int64(4000000 + i)
		members = append(members, id)
		f.register([8]byte{byte(40 + i)}, id, domain.PushTokenAPNs, "slow-member", nil, presenceNone)
	}
	f.members.members[groupID] = members
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.register([8]byte{3}, bobID, domain.PushTokenVoIP, "voip-bob", testSecret(), presenceNone)
	f.start()
	for range 4 {
		f.svc.ChannelMessage(groupTextFromAlice("busy"))
	}
	f.svc.NewMessage(textFromAlice("private"))
	waitForToken(t, f.sender, "tok-bob", time.Second)
	enc := &layerEncoder{layers: map[[8]byte]int{{3}: 228}}
	f.svc.IncomingCall(incomingCallFromAlice(enc.encode))
	waitForToken(t, f.sender, "voip-bob", time.Second)
}

// 重试的退避等待不占 worker：唯一的 worker 在等重试时，下一条推送照常立即发出。
func TestRetryBackoffDoesNotHoldTheWorker(t *testing.T) {
	f := newServiceFixture(t, Config{Workers: 1, UrgentWorkers: -1, MaxRetries: 1, RetryBackoff: 2 * time.Second})
	f.sender.respond = func(n Notification) (Result, error) {
		if n.Token == "tok-flaky" {
			return Result{StatusCode: 503, Reason: "ServiceUnavailable"}, nil
		}
		return Result{StatusCode: 200}, nil
	}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-flaky", nil, presenceNone)
	f.start()
	f.svc.NewMessage(textFromAlice("first"))
	waitForToken(t, f.sender, "tok-flaky", time.Second)

	f.register([8]byte{3}, aliceID, domain.PushTokenAPNs, "tok-alice", nil, presenceNone)
	msg := textFromAlice("to alice")
	msg.Chat, msg.SenderID, msg.Recipients = Chat{Kind: ChatPrivate, ID: bobID}, bobID, []int64{aliceID}
	f.svc.NewMessage(msg)
	waitForToken(t, f.sender, "tok-alice", 500*time.Millisecond)
}

func stopWithin(t *testing.T, f *serviceFixture, within time.Duration) {
	t.Helper()
	stopped := make(chan struct{})
	go func() {
		f.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(within):
		t.Fatalf("Run did not return within %v of shutdown", within)
	}
}

// 关停时不再重试：等待中的重试被放弃，Run 立即返回。
func TestShutdownAbandonsPendingRetries(t *testing.T) {
	f := newServiceFixture(t, Config{MaxRetries: 3, RetryBackoff: 10 * time.Second})
	f.sender.respond = func(Notification) (Result, error) {
		return Result{StatusCode: 503, Reason: "ServiceUnavailable"}, nil
	}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()
	f.svc.NewMessage(textFromAlice("hello"))
	waitUntil(t, func() bool { return f.sentCount() == 1 })
	stopWithin(t, f, time.Second)
	time.Sleep(50 * time.Millisecond)
	if got := f.sentCount(); got != 1 {
		t.Fatalf("send attempts = %d, want 1: no retry after shutdown", got)
	}
}

// 关停有期限：卡在网关上的发送在 ShutdownTimeout 之后被取消，Run 返回。
func TestShutdownDeadlineCancelsStuckSends(t *testing.T) {
	f := newServiceFixture(t, Config{SendTimeout: time.Hour, ShutdownTimeout: 50 * time.Millisecond})
	gate := make(chan struct{})
	defer close(gate)
	f.svc.sender = gatedSender{recordingSender: f.sender, gate: gate}
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "slow-bob", nil, presenceNone)
	f.start()
	f.svc.NewMessage(textFromAlice("hello"))
	waitUntil(t, func() bool { return len(f.svc.urgent) == 0 })
	stopWithin(t, f, time.Second)
}

// 关停时群组扇出不再翻下一页成员。
func TestShutdownStopsChannelPaging(t *testing.T) {
	f := newChannelFixture(t, Config{Workers: 1, MemberBatch: 1, SendTimeout: time.Hour, ShutdownTimeout: time.Second})
	gate := make(chan struct{})
	f.svc.sender = gatedSender{recordingSender: f.sender, gate: gate}
	var members []int64
	for i := range 5 {
		id := int64(4100000 + i)
		members = append(members, id)
		f.register([8]byte{byte(60 + i)}, id, domain.PushTokenAPNs, "slow-member", nil, presenceNone)
	}
	f.members.members[groupID] = members
	f.start()
	f.svc.ChannelMessage(groupTextFromAlice("hi"))
	// 第一页已在发送（卡住），下一页已放回队列。
	waitUntil(t, func() bool {
		f.members.mu.Lock()
		defer f.members.mu.Unlock()
		return len(f.members.calls) == 1
	})
	stopped := make(chan struct{})
	go func() {
		f.stop()
		close(stopped)
	}()
	time.Sleep(20 * time.Millisecond)
	close(gate)
	<-stopped
	f.members.mu.Lock()
	defer f.members.mu.Unlock()
	if len(f.members.calls) > 2 {
		t.Fatalf("member pages read during shutdown = %d, want paging stopped", len(f.members.calls))
	}
}

// 群组推送遇到限流（429）时，重试回到批量 lane、指标仍记为 channel：通用 worker 被扇出占满时
// 重试排在后面，紧急专用 worker 只处理私聊和来电，不被群组重试挤占。
func TestChannelRetryStaysOnBulkLane(t *testing.T) {
	f := newChannelFixture(t, Config{Workers: 1, UrgentWorkers: 1, MaxRetries: 1, RetryBackoff: time.Millisecond, SendTimeout: time.Hour})
	gate := make(chan struct{})
	var closeGate sync.Once
	defer closeGate.Do(func() { close(gate) })
	f.svc.sender = gatedSender{recordingSender: f.sender, gate: gate}
	var throttledOnce sync.Once
	f.sender.respond = func(n Notification) (Result, error) {
		throttled := false
		if n.Token == "tok-carol" {
			throttledOnce.Do(func() { throttled = true })
		}
		if throttled {
			return Result{StatusCode: 429, Reason: "TooManyRequests"}, nil
		}
		return Result{StatusCode: 200}, nil
	}
	// carol 先发（被限流、安排重试），随后 dave 的发送卡住唯一的通用 worker。
	f.members.members[groupID] = []int64{aliceID, carolID, daveID}
	f.register([8]byte{3}, carolID, domain.PushTokenAPNs, "tok-carol", nil, presenceNone)
	f.register([8]byte{4}, daveID, domain.PushTokenAPNs, "slow-dave", nil, presenceNone)
	f.register([8]byte{2}, bobID, domain.PushTokenAPNs, "tok-bob", nil, presenceNone)
	f.start()
	f.svc.ChannelMessage(groupTextFromAlice("hi"))
	waitForToken(t, f.sender, "tok-carol", time.Second)

	f.svc.NewMessage(textFromAlice("private"))
	waitForToken(t, f.sender, "tok-bob", time.Second)
	time.Sleep(30 * time.Millisecond)
	if got := f.sendsTo("tok-carol"); got != 1 {
		t.Fatalf("tok-carol sends while the bulk lane is busy = %d, want 1: the retry must wait on the bulk lane", got)
	}

	closeGate.Do(func() { close(gate) })
	waitUntil(t, func() bool { return f.sendsTo("tok-carol") == 2 })
	f.stop()
	f.metrics.mu.Lock()
	defer f.metrics.mu.Unlock()
	if want := []string{"channel", "message", "channel", "channel"}; !slices.Equal(f.metrics.sentKinds, want) {
		t.Fatalf("send kinds = %v, want %v", f.metrics.sentKinds, want)
	}
}

func (f *channelFixture) sendsTo(token string) int {
	f.sender.mu.Lock()
	defer f.sender.mu.Unlock()
	n := 0
	for _, sent := range f.sender.sent {
		if sent.Token == token {
			n++
		}
	}
	return n
}
