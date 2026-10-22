package memory

import (
	"context"
	"errors"
	"sync"
	"telesrv/internal/domain"
	"time"
)

type BotGuestQueryStore struct {
	mu       sync.Mutex
	queries  map[int64]domain.BotGuestQuery
	enabled  map[int64]bool
	feedback map[int64]int
}

func NewBotGuestQueryStore() *BotGuestQueryStore {
	return &BotGuestQueryStore{queries: make(map[int64]domain.BotGuestQuery), enabled: make(map[int64]bool), feedback: make(map[int64]int)}
}
func (s *BotGuestQueryStore) CreateGuestQuery(_ context.Context, q domain.BotGuestQuery) error {
	if q.ID == 0 || q.BotUserID <= 0 || q.CallerUserID <= 0 || q.MessageID <= 0 || !q.ExpiresAt.After(q.CreatedAt) || (q.Peer.Type != domain.PeerTypeUser && q.Peer.Type != domain.PeerTypeChannel) || q.Peer.ID <= 0 {
		return errors.New("QUERY_ID_INVALID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, old := range s.queries {
		if !old.ExpiresAt.After(q.CreatedAt) {
			delete(s.queries, id)
		}
	}
	if _, exists := s.queries[q.ID]; exists {
		return errors.New("QUERY_ID_INVALID")
	}
	s.queries[q.ID] = q
	return nil
}
func (s *BotGuestQueryStore) ConsumeGuestQuery(_ context.Context, botID, queryID int64, now time.Time) (domain.BotGuestQuery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.queries[queryID]
	if !ok {
		return domain.BotGuestQuery{}, false, nil
	}
	if !q.ExpiresAt.After(now) {
		delete(s.queries, queryID)
		return domain.BotGuestQuery{}, false, nil
	}
	if q.BotUserID != botID {
		return domain.BotGuestQuery{}, false, nil
	}
	delete(s.queries, queryID)
	return q, true, nil
}
func (s *BotGuestQueryStore) GetGuestQuery(_ context.Context, botID, queryID int64, now time.Time) (domain.BotGuestQuery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.queries[queryID]
	if !ok || q.BotUserID != botID || !q.ExpiresAt.After(now) {
		return domain.BotGuestQuery{}, false, nil
	}
	return q, true, nil
}
func (s *BotGuestQueryStore) GuestBotEnabled(_ context.Context, botID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enabled[botID], nil
}
func (s *BotGuestQueryStore) SetGuestBotEnabled(_ context.Context, botID int64, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled[botID] = enabled
	return nil
}
func (s *BotGuestQueryStore) GetInlineFeedback(_ context.Context, botID int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.feedback[botID], nil
}
func (s *BotGuestQueryStore) SetInlineFeedback(_ context.Context, botID int64, percentage int) error {
	if percentage < 0 || percentage > 100 {
		return errors.New("PERCENTAGE_INVALID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.feedback[botID] = percentage
	return nil
}
