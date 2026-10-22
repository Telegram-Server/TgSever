package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *StoryStore) CreateBotStory(ctx context.Context, req domain.StoryCreateRequest, owner domain.BotStoryOwner) (domain.StoryCreateResult, error) {
	if owner.BotID <= 0 || owner.BusinessConnectionID == "" {
		return domain.StoryCreateResult{}, domain.ErrStoryIDInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.StoryCreateResult{}, fmt.Errorf("create bot story: database does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.StoryCreateResult{}, err
	}
	defer tx.Rollback(ctx)
	result, err := NewStoryStore(tx).CreateStory(ctx, req)
	if err != nil {
		return result, err
	}
	if result.Duplicate {
		existing, err := NewStoryStore(tx).BotStoryOwner(ctx, req.Owner, result.Story.ID)
		if err != nil {
			return domain.StoryCreateResult{}, err
		}
		if existing != owner {
			return domain.StoryCreateResult{}, domain.ErrStoryIDInvalid
		}
	} else {
		err = sqlcgen.New(tx).InsertBotStoryOwner(ctx, sqlcgen.InsertBotStoryOwnerParams{
			OwnerPeerType: string(req.Owner.Type), OwnerPeerID: req.Owner.ID, StoryID: int32(result.Story.ID),
			BotUserID: owner.BotID, BusinessConnectionID: owner.BusinessConnectionID,
		})
		if err != nil {
			return domain.StoryCreateResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.StoryCreateResult{}, err
	}
	return result, nil
}

func (s *StoryStore) BotStoryOwner(ctx context.Context, peer domain.Peer, id int) (domain.BotStoryOwner, error) {
	row, err := sqlcgen.New(s.db).GetBotStoryOwner(ctx, sqlcgen.GetBotStoryOwnerParams{OwnerPeerType: string(peer.Type), OwnerPeerID: peer.ID, StoryID: int32(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BotStoryOwner{}, domain.ErrStoryNotFound
	}
	if err != nil {
		return domain.BotStoryOwner{}, err
	}
	return domain.BotStoryOwner{BotID: row.BotUserID, BusinessConnectionID: row.BusinessConnectionID}, nil
}
