package memory

import "telesrv/internal/domain"

func (s *MessageStore) BindGamesPassportStore(gp *GamesPassportStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gamesPassport = gp
}

// Caller owns message.mu; Passport never acquires it in the opposite order.
// The staged authorization becomes visible only with the actual recipient box.
func (s *MessageStore) preparePassportAuthorization(req domain.SendPrivateTextRequest) (commit, release func(), err error) {
	if req.Media == nil || req.Media.Kind != domain.MessageMediaKindService || req.Media.ServiceAction == nil || req.Media.ServiceAction.Kind != domain.MessageServiceActionPassportData {
		return nil, nil, nil
	}
	a := req.Media.ServiceAction.PassportData
	if s.gamesPassport == nil || a == nil || req.SenderUserID <= 0 || req.RecipientUserID != a.BotUserID || req.SenderUserID == a.BotUserID || req.RecipientBlocked {
		return nil, nil, domain.ErrPassportInvalid
	}
	gp := s.gamesPassport
	gp.mu.Lock()
	d := domain.ClonePassportUserData(gp.passport[req.SenderUserID])
	if err = d.Authorize(*domain.ClonePassportAuthorization(a)); err != nil {
		gp.mu.Unlock()
		return nil, nil, err
	}
	return func() { gp.passport[req.SenderUserID] = d }, gp.mu.Unlock, nil
}

func (s *MessageStore) prepareBusinessGameMessage(req domain.SendPrivateTextRequest) (commit, release func(), err error) {
	if req.Media == nil || req.Media.Kind != domain.MessageMediaKindGame || req.Media.Game == nil || req.BusinessBotID == 0 {
		return nil, nil, nil
	}
	if s.gamesPassport == nil || req.Media.Game.BotUserID != req.BusinessBotID || req.BusinessConnectionID == "" || req.RecipientBlocked {
		return nil, nil, domain.ErrPassportInvalid
	}
	gp := s.gamesPassport
	gp.mu.Lock()
	id := s.nextBox[req.SenderUserID] + 1
	key := [3]int64{req.BusinessBotID, req.RecipientUserID, int64(id)}
	ref := domain.BusinessGameMessageRef{BotUserID: req.BusinessBotID, ChatID: req.RecipientUserID, MessageID: id, OwnerUserID: req.SenderUserID, ConnectionID: req.BusinessConnectionID}
	return func() { gp.businessGames[key] = append(gp.businessGames[key], ref) }, gp.mu.Unlock, nil
}
