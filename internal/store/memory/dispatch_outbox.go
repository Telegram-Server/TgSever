package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"telesrv/internal/store"
)

// DispatchOutboxStore 是 store.DispatchOutboxStore 的内存实现：把 UpdateEventStore 里随事件
// 一起记下的投递任务（AppendAllocatedWithDispatch、私聊发送等）交给 rpc.OutboxDispatcher，
// 让内存服务端（端到端测试）也像生产一样把落库的 update 推给在线会话。
//
// 内存 outbox 只投递一次：领取即视为已交付，MarkFailed 不重试（在线推送失败的会话
// 重连后照常经 getDifference 补齐）。同一用户的任务按 pts 顺序领取。
type DispatchOutboxStore struct {
	events *UpdateEventStore

	mu      sync.Mutex
	claimed map[int64]int // 每个用户已领取的 dispatches 条数
	nextID  int64
}

var _ store.DispatchOutboxStore = (*DispatchOutboxStore)(nil)

// NewDispatchOutboxStore 创建读取 events 投递任务的内存 outbox。
func NewDispatchOutboxStore(events *UpdateEventStore) *DispatchOutboxStore {
	return &DispatchOutboxStore{events: events, claimed: make(map[int64]int)}
}

func (s *DispatchOutboxStore) ClaimPending(_ context.Context, limit int) ([]store.DispatchOutboxItem, error) {
	if s == nil || s.events == nil || limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events.mu.RLock()
	defer s.events.mu.RUnlock()
	userIDs := make([]int64, 0, len(s.events.dispatches))
	for userID, dispatches := range s.events.dispatches {
		if len(dispatches) > s.claimed[userID] {
			userIDs = append(userIDs, userID)
		}
	}
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })
	out := make([]store.DispatchOutboxItem, 0, limit)
	for _, userID := range userIDs {
		dispatches := s.events.dispatches[userID]
		for s.claimed[userID] < len(dispatches) && len(out) < limit {
			d := dispatches[s.claimed[userID]]
			s.claimed[userID]++
			s.nextID++
			item := store.DispatchOutboxItem{
				ID:               s.nextID,
				TargetUserID:     userID,
				Pts:              d.Pts,
				ExcludeAuthKeyID: d.ExcludeAuthKeyID,
				ExcludeSessionID: d.ExcludeSessionID,
			}
			// 待领取的任务几乎总是最新的事件，从后往前找。
			events := s.events.events[userID]
			for i := len(events) - 1; i >= 0; i-- {
				if events[i].Pts == d.Pts {
					item.EventType = events[i].Type
					break
				}
			}
			out = append(out, item)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *DispatchOutboxStore) MarkDelivered(context.Context, store.DispatchOutboxItem) error {
	return nil
}

func (s *DispatchOutboxStore) MarkFailed(context.Context, store.DispatchOutboxItem, string) error {
	return nil
}

func (s *DispatchOutboxStore) DeleteFailed(context.Context, time.Duration, int) (int, error) {
	return 0, nil
}
