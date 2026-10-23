package memory

import (
	"context"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// AttachBotUpdateStore 让拉黑名单变动在同一临界区里写 updateBotStopped 记录（#148）。
func (s *ContactStore) AttachBotUpdateStore(updates *BotUpdateStore) {
	s.mu.Lock()
	s.botUpdates = updates
	s.mu.Unlock()
}

// appendBlocklistBotUpdatesLocked 在 MutateBlocklist 持锁期间写入对 bot 的拉黑/解除拉黑。
func (s *ContactStore) appendBlocklistBotUpdatesLocked(snapshot store.BlocklistMutationSnapshot) ([]domain.BotUpdate, error) {
	updates := store.BlocklistBotUpdates(snapshot)
	if s.botUpdates == nil || len(updates) == 0 {
		return nil, nil
	}
	return s.botUpdates.AppendBotUpdates(context.Background(), updates)
}
