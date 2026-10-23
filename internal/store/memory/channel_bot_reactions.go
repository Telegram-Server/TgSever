package memory

import (
	"context"
	"sort"

	"telesrv/internal/domain"
)

// SetChannelMessageReactions 设置反应，并在同一临界区（botUpdateMu）里为能收到的 bot 写
// updateBotMessageReaction / updateBotMessageReactions 记录（#150）。
func (s *ChannelStore) SetChannelMessageReactions(ctx context.Context, req domain.SetChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error) {
	s.botUpdateMu.Lock()
	defer s.botUpdateMu.Unlock()
	s.mu.RLock()
	var old []domain.MessageReaction
	for _, row := range s.reactions[req.ChannelID][req.MessageID][req.UserID] {
		old = append(old, row.Reaction)
	}
	s.mu.RUnlock()
	res, err := s.setChannelMessageReactions(ctx, req)
	if err != nil || s.botUpdates == nil {
		return res, err
	}
	var current []domain.MessageReaction
	var botMembers []domain.ChannelMember
	s.mu.RLock()
	for _, row := range s.reactions[req.ChannelID][req.MessageID][req.UserID] {
		current = append(current, row.Reaction)
	}
	for _, member := range s.members[req.ChannelID] {
		if s.botUpdates.isBot(member.UserID) {
			botMembers = append(botMembers, member)
		}
	}
	s.mu.RUnlock()
	sort.Slice(botMembers, func(i, j int) bool { return botMembers[i].UserID < botMembers[j].UserID })
	date := botUpdateDate(req.Date)
	updates := domain.BotChannelMessageReactionChanges(res.Channel, req.MessageID, req.UserID, s.botUpdates.isBot(req.UserID), old, current, res.Reactions.Results, botMembers, date)
	res.BotUpdates, err = s.botUpdates.appendBotOnly(updates)
	return res, err
}
