package memory

import (
	"context"

	"telesrv/internal/domain"
)

func (s *BotStore) SetBotCommandSet(ctx context.Context, botUserID int64, key domain.BotCommandSetKey, commands []domain.BotCommand) (int, error) {
	if key.IsDefault() {
		return s.UpdateBotCommands(ctx, botUserID, commands)
	}
	if !key.Scope.Valid() || !domain.ValidBotCommandLangCode(key.LangCode) {
		return 0, domain.ErrBotCommandInvalid
	}
	s.mu.Lock()
	if _, ok := s.byID[botUserID]; !ok {
		s.mu.Unlock()
		return 0, domain.ErrBotNotFound
	}
	sets := s.commandSets[botUserID]
	if len(commands) == 0 {
		delete(sets, key)
	} else {
		if _, exists := sets[key]; !exists && len(sets) >= domain.MaxBotCommandSets {
			s.mu.Unlock()
			return 0, domain.ErrBotCommandSetsTooMany
		}
		if sets == nil {
			sets = make(map[domain.BotCommandSetKey][]domain.BotCommand)
			s.commandSets[botUserID] = sets
		}
		sets[key] = append([]domain.BotCommand(nil), commands...)
	}
	s.mu.Unlock()
	ver, ok := s.users.bumpBotInfoVersion(botUserID)
	if !ok {
		return 0, domain.ErrBotNotFound
	}
	return ver, nil
}

func (s *BotStore) GetBotCommandSet(ctx context.Context, botUserID int64, key domain.BotCommandSetKey) ([]domain.BotCommand, bool, error) {
	if key.IsDefault() {
		profile, found, err := s.GetBot(ctx, botUserID)
		if err != nil || !found || len(profile.Commands) == 0 {
			return nil, false, err
		}
		return profile.Commands, true, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	commands, ok := s.commandSets[botUserID][key]
	if !ok {
		return nil, false, nil
	}
	return append([]domain.BotCommand(nil), commands...), true, nil
}

func (s *BotStore) ListBotCommandSetsForPeer(_ context.Context, botUserIDs []int64, peer domain.Peer, viewerUserID int64) ([]domain.BotCommandSet, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.BotCommandSet
	for _, botUserID := range botUserIDs {
		for key, commands := range s.commandSets[botUserID] {
			scope := key.Scope
			if scope.Peer.ID != 0 && (scope.Peer != peer || (scope.Type == domain.BotCommandScopePeerUser && scope.UserID != viewerUserID)) {
				continue
			}
			out = append(out, domain.BotCommandSet{
				BotUserID: botUserID,
				Key:       key,
				Commands:  append([]domain.BotCommand(nil), commands...),
			})
		}
	}
	return out, nil
}
