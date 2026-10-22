package memory

import (
	"context"
	"errors"
	"sort"
	"sync"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type GamesPassportStore struct {
	mu            sync.Mutex
	games         map[int64]domain.BotGame
	scores        map[domain.GameScoreScope]map[int64]int
	passport      map[int64]domain.PassportUserData
	publicKeys    map[int64]string
	businessGames map[[3]int64][]domain.BusinessGameMessageRef
}

var _ store.BotGamesPassportStore = (*GamesPassportStore)(nil)

func NewGamesPassportStore() *GamesPassportStore {
	return &GamesPassportStore{games: map[int64]domain.BotGame{}, scores: map[domain.GameScoreScope]map[int64]int{}, passport: map[int64]domain.PassportUserData{}, publicKeys: map[int64]string{}, businessGames: map[[3]int64][]domain.BusinessGameMessageRef{}}
}
func (s *GamesPassportStore) CreateBotGame(_ context.Context, g domain.BotGame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.games {
		if old.ID == g.ID || old.BotUserID == g.BotUserID && old.ShortName == g.ShortName {
			return errors.New("GAME_SHORTNAME_OCCUPIED")
		}
	}
	s.games[g.ID] = domain.CloneBotGame(g)
	return nil
}
func (s *GamesPassportStore) GetBotGame(_ context.Context, botID int64, name string) (domain.BotGame, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.games {
		if g.BotUserID == botID && g.ShortName == name {
			return domain.CloneBotGame(g), true, nil
		}
	}
	return domain.BotGame{}, false, nil
}
func (s *GamesPassportStore) GetBotGameByID(_ context.Context, id int64) (domain.BotGame, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.games[id]
	return domain.CloneBotGame(g), ok, nil
}
func (s *GamesPassportStore) SetBotGameScore(_ context.Context, key domain.GameScoreScope, userID int64, score int, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	scores := s.scores[key]
	old, found := scores[userID]
	if found && !force && score <= old {
		return domain.ErrGameScoreNotModified
	}
	if scores == nil {
		scores = map[int64]int{}
		s.scores[key] = scores
	}
	scores[userID] = score
	return nil
}
func (s *GamesPassportStore) BotGameScores(_ context.Context, key domain.GameScoreScope) ([]domain.GameScore, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.GameScore, 0, len(s.scores[key]))
	for id, score := range s.scores[key] {
		out = append(out, domain.GameScore{UserID: id, Score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].UserID < out[j].UserID
	})
	for i := range out {
		out[i].Position = i + 1
	}
	return out, nil
}
func (s *GamesPassportStore) GetPassportData(_ context.Context, id int64) (domain.PassportUserData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return domain.ClonePassportUserData(s.passport[id]), nil
}
func (s *GamesPassportStore) SavePassportElement(_ context.Context, id int64, el domain.PassportElement) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.passport[id]
	d.SaveElement(domain.ClonePassportElements([]domain.PassportElement{el})[0])
	s.passport[id] = d
	return nil
}
func (s *GamesPassportStore) DeletePassportElements(_ context.Context, id int64, types []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.passport[id]
	d.DeleteElements(types)
	s.passport[id] = d
	return nil
}
func (s *GamesPassportStore) SetPassportErrors(_ context.Context, id, botID int64, errs []domain.PassportError) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.passport[id]
	cloned := append([]domain.PassportError(nil), errs...)
	for i := range cloned {
		cloned[i].FileHashes = append([]string(nil), cloned[i].FileHashes...)
	}
	if err := d.SetErrors(botID, cloned); err != nil {
		return err
	}
	s.passport[id] = d
	return nil
}
func (s *GamesPassportStore) SetPassportPublicKey(_ context.Context, id int64, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publicKeys[id] = key
	return nil
}
func (s *GamesPassportStore) PassportPublicKey(_ context.Context, id int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publicKeys[id], nil
}
func (s *GamesPassportStore) BusinessGameMessageRefs(_ context.Context, botID, chatID int64, id int) ([]domain.BusinessGameMessageRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domain.BusinessGameMessageRef(nil), s.businessGames[[3]int64{botID, chatID, int64(id)}]...), nil
}
