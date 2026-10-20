package storetest

import (
	"context"
	"slices"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
)

type ReactionChannelStore interface {
	store.ChannelStore
	DeleteChannelParticipantReaction(context.Context, domain.DeleteChannelParticipantReactionRequest) (domain.ChannelMessageReactionsResult, error)
	DeleteChannelParticipantReactions(context.Context, domain.DeleteChannelParticipantReactionsRequest) (domain.DeleteChannelParticipantReactionsResult, error)
}
type ReactionChangeFixture struct {
	Messages      store.MessageStore
	Channels      ReactionChannelStore
	Owner, Member int64
}

func ReactionChanges(t *testing.T, f ReactionChangeFixture) {
	t.Helper()
	ctx := context.Background()
	thumb := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}
	fire := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "🔥"}
	check := func(change domain.MessageReactionChange, actor int64, old, next []domain.MessageReaction) {
		t.Helper()
		if change.ActorUserID != actor || !slices.Equal(change.OldReactions, old) || !slices.Equal(change.NewReactions, next) {
			t.Fatalf("change=%+v, want actor=%d old=%v new=%v", change, actor, old, next)
		}
	}
	sent, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 22001, Message: "private", Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	private := func(actor, peer int64, id int, reactions []domain.MessageReaction) domain.PrivateMessageReactionsResult {
		t.Helper()
		res, err := f.Messages.SetMessageReactions(ctx, domain.SetPrivateMessageReactionsRequest{UserID: actor, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peer}, MessageID: id, Reactions: reactions, Big: true, Date: 1700000001})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	check(private(f.Member, f.Owner, sent.RecipientMessage.ID, []domain.MessageReaction{thumb}).MessageReactionChange, f.Member, nil, []domain.MessageReaction{thumb})
	private(f.Owner, f.Member, sent.SenderMessage.ID, []domain.MessageReaction{fire})
	changed := private(f.Member, f.Owner, sent.RecipientMessage.ID, []domain.MessageReaction{fire})
	check(changed.MessageReactionChange, f.Member, []domain.MessageReaction{thumb}, []domain.MessageReaction{fire})
	if changed.MessageID != sent.RecipientMessage.ID {
		t.Fatalf("private actor box id=%d", changed.MessageID)
	}
	check(private(f.Member, f.Owner, sent.RecipientMessage.ID, nil).MessageReactionChange, f.Member, []domain.MessageReaction{fire}, nil)
	service, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 22003, Date: 1700000002, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionHistoryClear}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Messages.SetMessageReactions(ctx, domain.SetPrivateMessageReactionsRequest{UserID: f.Member, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.Owner}, MessageID: service.RecipientMessage.ID, Reactions: []domain.MessageReaction{thumb}}); err != domain.ErrMessageIDInvalid {
		t.Fatalf("service reaction err=%v", err)
	}
	created, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: f.Owner, Title: "Reaction contract", Megagroup: true, MemberUserIDs: []int64{f.Member}, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	message, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: f.Owner, ChannelID: created.Channel.ID, RandomID: 22002, Message: "group", Date: 1700000001})
	if err != nil {
		t.Fatal(err)
	}
	channel := func(actor int64, reactions []domain.MessageReaction) domain.ChannelMessageReactionsResult {
		t.Helper()
		res, err := f.Channels.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{UserID: actor, ChannelID: created.Channel.ID, MessageID: message.Message.ID, Reactions: reactions, Big: true, Date: 1700000002})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	check(channel(f.Member, []domain.MessageReaction{thumb}).MessageReactionChange, f.Member, nil, []domain.MessageReaction{thumb})
	channel(f.Owner, []domain.MessageReaction{fire})
	check(channel(f.Member, []domain.MessageReaction{fire}).MessageReactionChange, f.Member, []domain.MessageReaction{thumb}, []domain.MessageReaction{fire})
	deleted, err := f.Channels.DeleteChannelParticipantReaction(ctx, domain.DeleteChannelParticipantReactionRequest{UserID: f.Owner, ChannelID: created.Channel.ID, MessageID: message.Message.ID, ParticipantUserID: f.Member})
	if err != nil {
		t.Fatal(err)
	}
	check(deleted.MessageReactionChange, f.Member, []domain.MessageReaction{fire}, nil)
	channel(f.Member, []domain.MessageReaction{thumb})
	bulk, err := f.Channels.DeleteChannelParticipantReactions(ctx, domain.DeleteChannelParticipantReactionsRequest{UserID: f.Owner, ChannelID: created.Channel.ID, ParticipantUserID: f.Member})
	if err != nil {
		t.Fatal(err)
	}
	if bulk.Deleted != 1 || len(bulk.Changes) != 1 {
		t.Fatalf("bulk result=%+v", bulk)
	}
	check(bulk.Changes[0], f.Member, []domain.MessageReaction{thumb}, nil)
	if len(bulk.Messages) != 1 || bulk.Messages[0].Reactions == nil || len(bulk.Messages[0].Reactions.Results) != 1 || bulk.Messages[0].Reactions.Results[0].Reaction != fire || bulk.Messages[0].Reactions.Results[0].Count != 1 {
		t.Fatalf("moderation removed another actor's reaction: %+v", bulk.Messages)
	}
}
