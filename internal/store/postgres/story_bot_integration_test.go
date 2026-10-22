package postgres

import (
	"context"
	"telesrv/internal/domain"
	"testing"
)

func TestBotStoryProvenanceAtomicityPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	owner, bot := createStoryTestUsers(t, ctx, pool)
	s := NewStoryStore(pool)
	req := domain.StoryCreateRequest{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, RandomID: 8001, Date: 1800000000, Period: 21600, Public: true, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 8001}}}
	// Ownership insert fails its FK. The preceding story insert must roll back too.
	if _, err := s.CreateBotStory(ctx, req, domain.BotStoryOwner{BotID: 9223372036854775807, BusinessConnectionID: "invalid"}); err == nil {
		t.Fatal("nonexistent bot accepted")
	}
	active, err := s.ListOwnerActiveStories(ctx, req.Owner, 1800000000, 100)
	if err != nil || len(active.Stories) != 0 {
		t.Fatalf("failed post left a visible story: %+v, %v", active, err)
	}
	provenance := domain.BotStoryOwner{BotID: bot.ID, BusinessConnectionID: "connection"}
	created, err := s.CreateBotStory(ctx, req, provenance)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.CreateBotStory(ctx, req, provenance)
	if err != nil || !duplicate.Duplicate || duplicate.Story.ID != created.Story.ID {
		t.Fatalf("duplicate = %+v, %v", duplicate, err)
	}
	if _, err := s.CreateBotStory(ctx, req, domain.BotStoryOwner{BotID: owner.ID, BusinessConnectionID: "hijack"}); err == nil {
		t.Fatal("duplicate reassigned original bot ownership")
	}
	got, err := NewStoryStore(pool).BotStoryOwner(ctx, req.Owner, created.Story.ID)
	if err != nil || got != provenance {
		t.Fatalf("persisted owner = %+v, %v", got, err)
	}
}
