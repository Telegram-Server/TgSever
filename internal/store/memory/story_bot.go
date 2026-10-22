package memory

import (
	"context"
	"telesrv/internal/domain"
)

func (s *StoryStore) CreateBotStory(_ context.Context, req domain.StoryCreateRequest, owner domain.BotStoryOwner) (domain.StoryCreateResult, error) {
	if err := validateStoryPeer(req.Owner); err != nil {
		return domain.StoryCreateResult{}, err
	}
	if req.RandomID == 0 || owner.BotID <= 0 || owner.BusinessConnectionID == "" {
		return domain.StoryCreateResult{}, domain.ErrStoryIDInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.createStoryLocked(req)
	if err != nil {
		return result, err
	}
	key := keyForStory(result.Story.Owner, result.Story.ID)
	if result.Duplicate {
		if s.botOwners[key] != owner {
			return domain.StoryCreateResult{}, domain.ErrStoryIDInvalid
		}
		return result, nil
	}
	if s.botOwners == nil {
		s.botOwners = make(map[storyKey]domain.BotStoryOwner)
	}
	s.botOwners[key] = owner
	return result, nil
}

func (s *StoryStore) BotStoryOwner(_ context.Context, peer domain.Peer, id int) (domain.BotStoryOwner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	owner, found := s.botOwners[keyForStory(peer, id)]
	if !found {
		return domain.BotStoryOwner{}, domain.ErrStoryNotFound
	}
	return owner, nil
}
