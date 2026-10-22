package memory

import (
	"container/heap"
	"context"
	"sync"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"time"
)

type messageDraftScope struct {
	botID, userID int64
	threadID      int
}
type messageDraftKey struct {
	scope messageDraftScope
	id    int64
}
type messageDraftEntry struct {
	draft      domain.MessageDraft
	stopped    bool
	finished   bool
	generation uint64
}
type messageDraftExpiry struct {
	key        messageDraftKey
	at         time.Time
	generation uint64
}
type messageDraftHeap []messageDraftExpiry

func (h messageDraftHeap) Len() int           { return len(h) }
func (h messageDraftHeap) Less(i, j int) bool { return h[i].at.Before(h[j].at) }
func (h messageDraftHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *messageDraftHeap) Push(v any)        { *h = append(*h, v.(messageDraftExpiry)) }
func (h *messageDraftHeap) Pop() any {
	old := *h
	n := len(old)
	v := old[n-1]
	old[n-1] = messageDraftExpiry{}
	*h = old[:n-1]
	return v
}

// MessageDraftStore is the process-local counterpart of the Redis transient
// store. ponytail: one lock; shard by scope only if local-test throughput needs it.
type MessageDraftStore struct {
	mu         sync.Mutex
	active     map[messageDraftScope]int64
	entries    map[messageDraftKey]messageDraftEntry
	expiry     messageDraftHeap
	generation uint64
}

func NewMessageDraftStore() *MessageDraftStore {
	return &MessageDraftStore{active: make(map[messageDraftScope]int64), entries: make(map[messageDraftKey]messageDraftEntry)}
}

func (s *MessageDraftStore) prune(now time.Time) {
	for len(s.expiry) > 0 && !now.Before(s.expiry[0].at) {
		item := heap.Pop(&s.expiry).(messageDraftExpiry)
		entry, ok := s.entries[item.key]
		if !ok || entry.generation != item.generation {
			continue
		}
		delete(s.entries, item.key)
		if s.active[item.key.scope] == item.key.id {
			delete(s.active, item.key.scope)
		}
	}
}

func (s *MessageDraftStore) put(key messageDraftKey, entry messageDraftEntry) {
	s.generation++
	entry.generation = s.generation
	s.entries[key] = entry
	heap.Push(&s.expiry, messageDraftExpiry{key: key, at: entry.draft.ExpiresAt, generation: entry.generation})
}

func (s *MessageDraftStore) ReplaceMessageDraft(_ context.Context, draft domain.MessageDraft, now time.Time) error {
	if err := draft.Validate(now); err != nil {
		return err
	}
	key := messageDraftKey{messageDraftScope{draft.BotUserID, draft.UserID, draft.ThreadID}, draft.ID}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	if entry, ok := s.entries[key]; ok && entry.stopped {
		return domain.ErrMessageDraftStopped
	}
	if id, ok := s.active[key.scope]; ok && id != key.id {
		delete(s.entries, messageDraftKey{key.scope, id})
	}
	draft.Action = append([]byte(nil), draft.Action...)
	s.put(key, messageDraftEntry{draft: draft})
	s.active[key.scope] = key.id
	return nil
}

func (s *MessageDraftStore) StopMessageDraft(_ context.Context, botID, userID int64, threadID int, draftID int64, now time.Time) (domain.MessageDraft, bool, error) {
	key := messageDraftKey{messageDraftScope{botID, userID, threadID}, draftID}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	entry, ok := s.entries[key]
	if !ok {
		return domain.MessageDraft{}, false, domain.ErrMessageDraftExpired
	}
	if entry.stopped {
		if entry.finished {
			return domain.MessageDraft{}, false, domain.ErrMessageDraftExpired
		}
		return domain.MessageDraft{}, false, nil
	}
	if s.active[key.scope] != draftID {
		return domain.MessageDraft{}, false, domain.ErrMessageDraftExpired
	}
	if !entry.draft.CanStop {
		return domain.MessageDraft{}, false, domain.ErrMessageDraftStopForbidden
	}
	draft := entry.draft
	draft.Action = append([]byte(nil), draft.Action...)
	entry.stopped = true
	entry.draft.Action = nil
	entry.draft.ExpiresAt = now.Add(domain.MessageDraftLifetime)
	s.put(key, entry)
	delete(s.active, key.scope)
	return draft, true, nil
}

func (s *MessageDraftStore) FinishMessageDraft(_ context.Context, botID, userID int64, threadID int, now time.Time) error {
	scope := messageDraftScope{botID, userID, threadID}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune(now)
	id, ok := s.active[scope]
	if !ok {
		return nil
	}
	key := messageDraftKey{scope, id}
	entry := s.entries[key]
	entry.stopped = true
	entry.finished = true
	entry.draft.Action = nil
	entry.draft.ExpiresAt = now.Add(domain.MessageDraftLifetime)
	s.put(key, entry)
	delete(s.active, scope)
	return nil
}

var _ store.MessageDraftStore = (*MessageDraftStore)(nil)
