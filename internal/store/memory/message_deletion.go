package memory

import (
	"context"
	"sync"

	"telesrv/internal/domain"
)

// MessageDeletionStore 是删除记录的内存实现，供 RPC 测试断言删除归属。
type MessageDeletionStore struct {
	mu        sync.Mutex
	rows      []domain.MessageDeletion
	nextID    int64
	nextBatch int64
}

func NewMessageDeletionStore() *MessageDeletionStore {
	return &MessageDeletionStore{}
}

// appendBatch 把一次删除操作的所有行写成同一批次。
func (s *MessageDeletionStore) appendBatch(rows []domain.MessageDeletion) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextBatch++
	for _, row := range rows {
		s.nextID++
		row.ID = s.nextID
		row.BatchID = s.nextBatch
		s.rows = append(s.rows, row)
	}
}

func (s *MessageDeletionStore) ListMessageDeletions(_ context.Context, filter domain.MessageDeletionFilter) ([]domain.MessageDeletion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.MessageDeletion, 0)
	for i := len(s.rows) - 1; i >= 0; i-- {
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
		row := s.rows[i]
		if filter.ActorUserID != 0 && row.ActorUserID != filter.ActorUserID {
			continue
		}
		if filter.SenderUserID != 0 && row.SenderUserID != filter.SenderUserID {
			continue
		}
		out = append(out, row)
	}
	return out, nil
}
