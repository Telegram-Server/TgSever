package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BotUpdateStore 是 bot qts 序列（#145）的内存实现。记录与 postgres 一样以 JSON 保存，
// 读出的总是独立的拷贝。users 用来判定成员变动的主体是不是 bot。
type BotUpdateStore struct {
	mu      sync.Mutex
	users   *UserStore
	qts     map[int64]int
	records map[int64][]memoryBotUpdate
}

type memoryBotUpdate struct {
	qts     int
	date    int
	payload []byte
}

var _ store.BotUpdateStore = (*BotUpdateStore)(nil)

// NewBotUpdateStore 创建内存 bot qts 序列；users 为 nil 时没有任何账号被视为 bot。
func NewBotUpdateStore(users *UserStore) *BotUpdateStore {
	return &BotUpdateStore{users: users, qts: make(map[int64]int), records: make(map[int64][]memoryBotUpdate)}
}

func (s *BotUpdateStore) AppendBotUpdates(_ context.Context, updates []domain.BotUpdate) ([]domain.BotUpdate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(updates)
}

// appendBotOnly 只写入主体是 bot 账号的记录：频道 store 在成员变动的临界区里调用它。
func (s *BotUpdateStore) appendBotOnly(updates []domain.BotUpdate) ([]domain.BotUpdate, error) {
	if s == nil || len(updates) == 0 {
		return nil, nil
	}
	kept := make([]domain.BotUpdate, 0, len(updates))
	for _, update := range updates {
		if s.isBot(update.BotUserID) {
			kept = append(kept, update)
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendLocked(kept)
}

func (s *BotUpdateStore) isBot(userID int64) bool {
	if s.users == nil || userID == 0 {
		return false
	}
	u, ok, _ := s.users.ByID(context.Background(), userID)
	return ok && u.Bot
}

func (s *BotUpdateStore) appendLocked(updates []domain.BotUpdate) ([]domain.BotUpdate, error) {
	encoded := make([][]byte, len(updates))
	for i, update := range updates {
		if update.BotUserID == 0 {
			return nil, fmt.Errorf("append bot update: missing bot user id")
		}
		payload, err := json.Marshal(update)
		if err != nil {
			return nil, fmt.Errorf("encode bot update: %w", err)
		}
		encoded[i] = payload
	}
	out := make([]domain.BotUpdate, 0, len(updates))
	for i, update := range updates {
		update.Qts = s.allocatedLocked(update.BotUserID) + 1
		s.qts[update.BotUserID] = update.Qts
		s.records[update.BotUserID] = append(s.records[update.BotUserID], memoryBotUpdate{qts: update.Qts, date: update.Date, payload: encoded[i]})
		out = append(out, update)
	}
	return out, nil
}

func (s *BotUpdateStore) ListBotUpdates(_ context.Context, botUserID int64, afterQts, limit int) ([]domain.BotUpdate, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.records[botUserID]
	start := sort.Search(len(rows), func(i int) bool { return rows[i].qts > afterQts })
	out := make([]domain.BotUpdate, 0, min(limit, len(rows)-start))
	for _, row := range rows[start:] {
		if len(out) == limit {
			break
		}
		var update domain.BotUpdate
		if err := json.Unmarshal(row.payload, &update); err != nil {
			return nil, fmt.Errorf("decode bot update: %w", err)
		}
		update.BotUserID, update.Qts, update.Date = botUserID, row.qts, row.date
		out = append(out, update)
	}
	return out, nil
}

func (s *BotUpdateStore) BotUpdatesState(_ context.Context, botUserID int64) (domain.BotUpdatesState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := domain.BotUpdatesState{Qts: s.allocatedLocked(botUserID)}
	if rows := s.records[botUserID]; len(rows) > 0 {
		st.MinRetainedQts = rows[0].qts
	}
	return st, nil
}

// allocatedLocked 返回 bot 已分配的最大 qts；还没有记录时是 domain.BotInitialQts。
func (s *BotUpdateStore) allocatedLocked(botUserID int64) int {
	if qts, ok := s.qts[botUserID]; ok {
		return qts
	}
	return domain.BotInitialQts
}

func (s *BotUpdateStore) DeleteExpiredBotUpdates(_ context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	cutoff := int(before.Unix())
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for botUserID, rows := range s.records {
		kept := rows[:0]
		for _, row := range rows {
			if row.date < cutoff && deleted < limit {
				deleted++
				continue
			}
			kept = append(kept, row)
		}
		if len(kept) == 0 {
			delete(s.records, botUserID)
		} else {
			s.records[botUserID] = kept
		}
	}
	return deleted, nil
}
