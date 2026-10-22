package channels

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func (s *Service) joinQueryStore() (store.ChatJoinQueryStore, error) {
	if s == nil {
		return nil, domain.ErrChannelInvalid
	}
	st, ok := s.channels.(store.ChatJoinQueryStore)
	if !ok {
		return nil, domain.ErrChannelInvalid
	}
	return st, nil
}
func (s *Service) SetChannelGuardBot(ctx context.Context, actor, channel, bot int64, enabled bool) (domain.Channel, error) {
	st, err := s.joinQueryStore()
	if err != nil {
		return domain.Channel{}, err
	}
	ch, err := st.SetChannelGuardBot(ctx, actor, channel, bot, enabled)
	return ch, err
}
func (s *Service) GetChannelGuardBot(ctx context.Context, channel int64) (int64, error) {
	st, err := s.joinQueryStore()
	if err != nil {
		return 0, err
	}
	return st.GetChannelGuardBot(ctx, channel)
}
func (s *Service) CreateChatJoinQuery(ctx context.Context, channel, bot, user int64, date, now int) (domain.ChatJoinQuery, error) {
	st, err := s.joinQueryStore()
	if err != nil {
		return domain.ChatJoinQuery{}, err
	}
	var raw [8]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return domain.ChatJoinQuery{}, err
	}
	id := int64(binary.LittleEndian.Uint64(raw[:]) & 0x7fffffffffffffff)
	if id == 0 {
		return domain.ChatJoinQuery{}, domain.ErrChatJoinQueryInvalid
	}
	return st.CreateChatJoinQuery(ctx, domain.ChatJoinQuery{ID: id, ChannelID: channel, BotUserID: bot, UserID: user, RequestDate: date, ExpiresAt: now + 10, State: "pending"})
}
func (s *Service) GetChatJoinQuery(ctx context.Context, id int64) (domain.ChatJoinQuery, error) {
	st, err := s.joinQueryStore()
	if err != nil {
		return domain.ChatJoinQuery{}, err
	}
	return st.GetChatJoinQuery(ctx, id)
}
func (s *Service) ResolveChatJoinQuery(ctx context.Context, bot, id int64, result, url string, now int) (domain.ChatJoinQuery, domain.CreateChannelResult, error) {
	st, err := s.joinQueryStore()
	if err != nil {
		return domain.ChatJoinQuery{}, domain.CreateChannelResult{}, err
	}
	q, res, err := st.ResolveChatJoinQuery(ctx, bot, id, result, url, now)
	if err == nil {
		s.invalidateActiveChannelIDs(activeMembershipUserIDsFromMembers(q.UserID, res.Members)...)
	}
	return q, res, err
}

func (s *Service) ExpireChatJoinQueries(ctx context.Context, now, limit int) ([]domain.ChatJoinQueryOutcome, error) {
	st, err := s.joinQueryStore()
	if err != nil {
		return nil, err
	}
	return st.ExpireChatJoinQueries(ctx, now, limit)
}
