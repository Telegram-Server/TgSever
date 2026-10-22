package storetest

import (
	"context"
	"slices"
	"telesrv/internal/domain"
	"testing"
)

func BotAPIReactionUpdates(t *testing.T, f BotAPIMemberUpdateFixture) {
	t.Helper()
	ctx := context.Background()
	thumb := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}
	fire := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "🔥"}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: 77001}
	payload := &domain.BotMessageReactionUpdated{Chat: peer, Date: baseNow(), MessageReactionChange: domain.MessageReactionChange{MessageID: 5, ActorUserID: f.OtherUserID, NewReactions: []domain.MessageReaction{thumb}}}
	req := domain.EnqueueBotAPIUpdateRequest{BotUserID: f.BotID, Kind: domain.BotAPIUpdateMessageReaction, Peer: peer, MessageID: 5, Date: payload.Date, Reaction: payload}
	if _, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req); err != nil || created {
		t.Fatalf("default reaction created=%v err=%v", created, err)
	}
	if err := f.Store.SetBotAPIAllowedUpdates(ctx, f.BotID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateMessageReaction, domain.BotAPIUpdateMessageReactionCount}); err != nil {
		t.Fatal(err)
	}
	first, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req)
	if err != nil || !created {
		t.Fatalf("first created=%v err=%v", created, err)
	}
	// Same actor/message/date transitions must not collapse into one queue row.
	payload = payload.Clone()
	payload.OldReactions, payload.NewReactions = []domain.MessageReaction{thumb}, []domain.MessageReaction{fire}
	req.Reaction = payload
	second, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req)
	if err != nil || !created || second.ID <= first.ID {
		t.Fatalf("second=%+v created=%v err=%v", second, created, err)
	}
	req.Kind = domain.BotAPIUpdateMessageReactionCount
	payload.Counts = []domain.ChannelMessageReactionCount{{Reaction: fire, Count: 2}}
	third, created, err := f.Store.EnqueueBotAPIUpdate(ctx, req)
	if err != nil || !created {
		t.Fatalf("count created=%v err=%v", created, err)
	}
	rows, err := f.Store.ListBotAPIUpdates(ctx, f.BotID, first.ID, 100)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].Reaction == nil || !slices.Equal(rows[0].Reaction.NewReactions, []domain.MessageReaction{thumb}) || rows[1].Reaction == nil || !slices.Equal(rows[1].Reaction.OldReactions, []domain.MessageReaction{thumb}) || !slices.Equal(rows[1].Reaction.NewReactions, []domain.MessageReaction{fire}) || rows[2].Reaction == nil || len(rows[2].Reaction.Counts) != 1 || rows[2].Reaction.Counts[0].Count != 2 {
		t.Fatalf("reaction snapshots=%+v", rows)
	}
	tail, err := f.Store.ListTailBotAPIUpdates(ctx, f.BotID, 1, 100)
	if err != nil || len(tail) != 1 || tail[0].ID != third.ID || tail[0].Reaction == nil || tail[0].Reaction.Counts[0].Count != 2 {
		t.Fatalf("tail=%+v err=%v", tail, err)
	}
}
