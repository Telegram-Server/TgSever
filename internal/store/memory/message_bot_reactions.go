package memory

import (
	"context"
	"sync"

	"telesrv/internal/domain"
)

// privateReactionBotUpdates 是私聊反应的 bot qts 写入（#150）：mu 把反应写入与 qts 记录
// 串成一个临界区，保证旧反应读取与写入之间没有别的反应插进来。
type privateReactionBotUpdates struct {
	mu      sync.Mutex
	updates *BotUpdateStore
}

// AttachBotUpdateStore 让私聊反应在同一临界区里写 bot qts 记录（#150）。
func (s *MessageStore) AttachBotUpdateStore(updates *BotUpdateStore) {
	s.reactionBots.mu.Lock()
	s.reactionBots.updates = updates
	s.reactionBots.mu.Unlock()
}

// SetMessageReactions 设置私聊消息反应；对话另一方是 bot 时为它写 updateBotMessageReaction。
func (s *MessageStore) SetMessageReactions(ctx context.Context, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	s.reactionBots.mu.Lock()
	defer s.reactionBots.mu.Unlock()
	bots := s.reactionBots.updates
	old := s.privateReactionsOf(req)
	res, err := s.setMessageReactions(ctx, req)
	if err != nil || bots == nil || req.Peer.ID == req.UserID || !bots.isBot(req.Peer.ID) {
		return res, err
	}
	botMessageID := 0
	for _, msg := range res.Messages {
		if msg.OwnerUserID == req.Peer.ID {
			botMessageID = msg.ID
		}
	}
	update, ok := domain.BotPrivateMessageReactionChange(req.Peer.ID, req.UserID, bots.isBot(req.UserID), botMessageID, old, s.privateReactionsOf(req), botUpdateDate(req.Date))
	if ok {
		res.BotUpdates, err = bots.appendBotOnly([]domain.BotUpdate{update})
	}
	return res, err
}

// privateReactionsOf 返回 req.UserID 当前在目标私聊消息上的反应（按选择顺序）。
func (s *MessageStore) privateReactionsOf(req domain.SetPrivateMessageReactionsRequest) []domain.MessageReaction {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, msg := range s.m[req.UserID] {
		if msg.ID != req.MessageID || msg.Peer != req.Peer || msg.UID == 0 {
			continue
		}
		var out []domain.MessageReaction
		for _, row := range s.privateReactions[msg.UID][req.UserID] {
			out = append(out, row.Reaction)
		}
		return out
	}
	return nil
}
