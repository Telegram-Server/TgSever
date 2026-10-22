package stories

import (
	"context"
	"telesrv/internal/domain"
)

type botStoryStore interface {
	CreateBotStory(context.Context, domain.StoryCreateRequest, domain.BotStoryOwner) (domain.StoryCreateResult, error)
	BotStoryOwner(context.Context, domain.Peer, int) (domain.BotStoryOwner, error)
}

func (s *Service) CreateBotStory(ctx context.Context, userID int64, req domain.StoryCreateRequest, owner domain.BotStoryOwner) (domain.StoryCreateResult, error) {
	if err := s.authorizeStoryOwner(ctx, userID, req.Owner, storyOwnerPost); err != nil {
		return domain.StoryCreateResult{}, err
	}
	if req.RandomID == 0 || req.Media == nil {
		return domain.StoryCreateResult{}, domain.ErrStoryIDInvalid
	}
	if !validStoryPeriod(req.Period) {
		return domain.StoryCreateResult{}, domain.ErrStoryPeriodInvalid
	}
	if s == nil {
		return domain.StoryCreateResult{}, domain.ErrStoryNotFound
	}
	backend, ok := s.stories.(botStoryStore)
	if !ok {
		return domain.StoryCreateResult{}, domain.ErrStoryNotFound
	}
	return backend.CreateBotStory(ctx, req, owner)
}

func (s *Service) BotStoryOwner(ctx context.Context, peer domain.Peer, id int) (domain.BotStoryOwner, error) {
	if s == nil {
		return domain.BotStoryOwner{}, domain.ErrStoryNotFound
	}
	backend, ok := s.stories.(botStoryStore)
	if !ok {
		return domain.BotStoryOwner{}, domain.ErrStoryNotFound
	}
	return backend.BotStoryOwner(ctx, peer, id)
}
