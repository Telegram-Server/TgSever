package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap/zaptest"
)

// TestBotAPIEnqueueDispatcherSynchronousBeforeRun 锁定未启动时的同步回退：
// 测试/未装配场景下 enqueue 行为与旧版完全一致（job 在调用方 goroutine 内即时执行）。
func TestBotAPIEnqueueDispatcherSynchronousBeforeRun(t *testing.T) {
	d := newBotAPIEnqueueDispatcher(zaptest.NewLogger(t), 4)
	ran := false
	d.Enqueue(context.Background(), func(context.Context) { ran = true })
	if !ran {
		t.Fatal("job must run synchronously before Run is called")
	}
}

// TestBotAPIEnqueueDispatcherFIFOOrder 锁定启动后单 worker FIFO：同一 bot 的
// update_id 顺序必须与 enqueue 顺序一致（乱序会让 bot 侧 getUpdates 看到错序消息）。
func TestBotAPIEnqueueDispatcherFIFOOrder(t *testing.T) {
	d := newBotAPIEnqueueDispatcher(zaptest.NewLogger(t), 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	for !d.started.Load() {
		time.Sleep(time.Millisecond)
	}

	var mu sync.Mutex
	var order []int
	done := make(chan struct{})
	for i := range 5 {
		d.Enqueue(context.Background(), func(context.Context) {
			mu.Lock()
			order = append(order, i)
			if len(order) == 5 {
				close(done)
			}
			mu.Unlock()
		})
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("jobs did not complete")
	}
	mu.Lock()
	defer mu.Unlock()
	for i, got := range order {
		if got != i {
			t.Fatalf("order = %v, want FIFO", order)
		}
	}
}

func startBotAPIEnqueueTestWorker(t *testing.T, d *botAPIEnqueueDispatcher) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	deadline := time.After(2 * time.Second)
	for !d.started.Load() {
		select {
		case <-deadline:
			cancel()
			t.Fatal("enqueue worker did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-d.done:
		case <-time.After(2 * time.Second):
			t.Error("enqueue worker did not drain")
		}
	})
	return cancel
}

func awaitBotAPIEnqueueTestSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for enqueue operation")
	}
}

func TestBotAPIEnqueueDispatcherPressurePreservesOrderAndDrains(t *testing.T) {
	d := newBotAPIEnqueueDispatcher(zaptest.NewLogger(t), 1)
	cancel := startBotAPIEnqueueTestWorker(t, d)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	entered := make(chan struct{})
	order := make(chan int, 4)
	d.Enqueue(context.Background(), func(context.Context) {
		close(entered)
		<-release
		order <- 1
	})
	awaitBotAPIEnqueueTestSignal(t, entered)
	reqCtx, cancelRequest := context.WithCancel(context.Background())
	d.Enqueue(reqCtx, func(ctx context.Context) {
		if ctx.Err() != nil {
			order <- -2
			return
		}
		order <- 2
	})
	cancelRequest() // Accepted work survives cancellation of its originating RPC.
	finished := make(chan struct{})
	go func() {
		d.Enqueue(context.Background(), func(ctx context.Context) {
			if ctx.Err() != nil {
				order <- -3
				return
			}
			order <- 3
		})
		close(finished)
	}()
	select {
	case <-finished:
		t.Fatal("full queue overtook blocked earlier work")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	unblock()
	awaitBotAPIEnqueueTestSignal(t, finished)
	awaitBotAPIEnqueueTestSignal(t, d.done)
	d.Enqueue(context.Background(), func(context.Context) { order <- 4 })
	for want := range 4 {
		select {
		case got := <-order:
			if got != want+1 {
				t.Fatalf("delivery order = %d, want %d", got, want+1)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("missing delivery %d", want+1)
		}
	}
}

func TestBotAPIEnqueueDispatcherPressureHonorsRequestCancellation(t *testing.T) {
	d := newBotAPIEnqueueDispatcher(zaptest.NewLogger(t), 1)
	startBotAPIEnqueueTestWorker(t, d)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	entered := make(chan struct{})
	d.Enqueue(context.Background(), func(context.Context) {
		close(entered)
		<-release
	})
	awaitBotAPIEnqueueTestSignal(t, entered)
	queued := make(chan struct{})
	d.Enqueue(context.Background(), func(context.Context) { close(queued) })
	reqCtx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	unaccepted := make(chan struct{}, 1)
	go func() {
		d.Enqueue(reqCtx, func(context.Context) { unaccepted <- struct{}{} })
		close(finished)
	}()
	cancel()
	awaitBotAPIEnqueueTestSignal(t, finished)
	select {
	case <-unaccepted:
		t.Fatal("canceled unaccepted work bypassed the queue")
	default:
	}
	select {
	case <-queued:
		t.Fatal("earlier queued work unexpectedly completed while blocked")
	default:
	}
}
