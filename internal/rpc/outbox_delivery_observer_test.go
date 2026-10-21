package rpc

import (
	"context"
	"errors"
	"sync"
	"testing"

	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type recordedDeliveries struct {
	mu   sync.Mutex
	list []OutboxDelivery
}

func (r *recordedDeliveries) observe(_ context.Context, d OutboxDelivery) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, d)
}

func observerTestEvent() (domain.UpdateEvent, store.DispatchOutboxItem) {
	msg := domain.Message{
		ID:          10,
		OwnerUserID: 1000000002,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		Date:        1700000300,
		Body:        "hello",
		Pts:         7,
	}
	event := domain.UpdateEvent{UserID: msg.OwnerUserID, Type: domain.UpdateEventNewMessage, Pts: msg.Pts, PtsCount: 1, Date: msg.Date, Message: msg}
	item := store.DispatchOutboxItem{ID: 55, TargetUserID: msg.OwnerUserID, Pts: msg.Pts, EventType: domain.UpdateEventNewMessage}
	return event, item
}

// 单条与批量两条投递路径都把“投递了哪条事件”交给观察者（推送据此触发）。
func TestOutboxDispatcherReportsDeliveriesToObserver(t *testing.T) {
	for _, batch := range []bool{false, true} {
		event, item := observerTestEvent()
		var events store.UpdateEventStore = &captureUpdateEventStore{events: []domain.UpdateEvent{event}}
		var outbox store.DispatchOutboxStore = &captureDispatchOutbox{items: []store.DispatchOutboxItem{item}}
		if batch {
			events = &batchEventStore{captureUpdateEventStore: events.(*captureUpdateEventStore)}
			outbox = &batchDispatchOutbox{captureDispatchOutbox: outbox.(*captureDispatchOutbox)}
		}
		rec := &recordedDeliveries{}
		dispatcher := newTestOutboxDispatcher(events, outbox, &captureSessions{}, zaptest.NewLogger(t), WithOutboxDeliveryObserver(rec.observe))
		dispatcher.DispatchOnce(context.Background())

		if len(rec.list) != 1 {
			t.Fatalf("batch=%v: observed %d deliveries, want 1", batch, len(rec.list))
		}
		got := rec.list[0]
		if got.Item.ID != item.ID || got.Event.Message.Body != "hello" {
			t.Fatalf("batch=%v: delivery = %+v, want item 55 with its event", batch, got)
		}
	}
}

// failingMarkOutbox 是标记 delivered 会失败的批量 outbox：批量标记一律失败，逐条标记按 failItem 决定。
type failingMarkOutbox struct {
	*captureDispatchOutbox
	failItem bool
}

func (s *failingMarkOutbox) MarkDeliveredBatch(context.Context, []store.DispatchOutboxItem) error {
	return errors.New("batch mark failed")
}

func (s *failingMarkOutbox) MarkDelivered(ctx context.Context, item store.DispatchOutboxItem) error {
	if s.failItem {
		return errors.New("mark failed")
	}
	return s.captureDispatchOutbox.MarkDelivered(ctx, item)
}

// 批量路径只把确实标记为 delivered 的事件交给观察者：没标记成功的行会在租约过期后重投，
// 那时再通知，否则同一条消息会推两次（与单条路径一致）。
func TestOutboxDispatcherBatchObservesOnlyItemsMarkedDelivered(t *testing.T) {
	for _, failItem := range []bool{true, false} {
		event, item := observerTestEvent()
		events := &batchEventStore{captureUpdateEventStore: &captureUpdateEventStore{events: []domain.UpdateEvent{event}}}
		outbox := &failingMarkOutbox{captureDispatchOutbox: &captureDispatchOutbox{items: []store.DispatchOutboxItem{item}}, failItem: failItem}
		rec := &recordedDeliveries{}
		dispatcher := newTestOutboxDispatcher(events, outbox, &captureSessions{}, zaptest.NewLogger(t), WithOutboxDeliveryObserver(rec.observe))
		dispatcher.DispatchOnce(context.Background())

		want := 1
		if failItem {
			want = 0
		}
		if len(rec.list) != want {
			t.Fatalf("per-item mark fails=%v: observed %d deliveries, want %d", failItem, len(rec.list), want)
		}
	}
}
