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
	t.Run("Bot API reply parameters", func(t *testing.T) { BotAPIReplyParameters(t, f); BotAPIChannelReplyParameters(t, f) })
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
	deleted, err := f.Channels.DeleteChannelParticipantReaction(ctx, domain.DeleteChannelParticipantReactionRequest{UserID: f.Owner, ChannelID: created.Channel.ID, MessageID: message.Message.ID, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: f.Member}})
	if err != nil {
		t.Fatal(err)
	}
	check(deleted.MessageReactionChange, f.Member, []domain.MessageReaction{fire}, nil)
	channel(f.Member, []domain.MessageReaction{thumb})
	bulk, err := f.Channels.DeleteChannelParticipantReactions(ctx, domain.DeleteChannelParticipantReactionsRequest{UserID: f.Owner, ChannelID: created.Channel.ID, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: f.Member}})
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
	t.Run("chat actor namespace and moderation", func(t *testing.T) {
		actor := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
		req := domain.SetChannelMessageReactionsRequest{UserID: f.Owner, Actor: actor, ChannelID: created.Channel.ID, MessageID: message.Message.ID, Reactions: []domain.MessageReaction{thumb}, Date: 1700000010}
		res, err := f.Channels.SetChannelMessageReactions(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if res.ActorChatID != actor.ID || res.ActorUserID != 0 || len(res.Reactions.Results) != 2 {
			t.Fatalf("chat actor selection = %+v", res)
		}
		list, err := f.Channels.ListChannelMessageReactions(ctx, domain.ChannelMessageReactionsListRequest{UserID: f.Owner, ChannelID: created.Channel.ID, MessageID: message.Message.ID, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		all := append([]domain.ChannelMessagePeerReaction(nil), list.Reactions...)
		if list.NextOffset != "" {
			page, err := f.Channels.ListChannelMessageReactions(ctx, domain.ChannelMessageReactionsListRequest{UserID: f.Owner, ChannelID: created.Channel.ID, MessageID: message.Message.ID, Limit: 10, Offset: list.NextOffset})
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, page.Reactions...)
		}
		if !slices.ContainsFunc(all, func(row domain.ChannelMessagePeerReaction) bool {
			return row.ActorChatID == actor.ID && row.UserID == 0 && row.Reaction == thumb
		}) {
			t.Fatalf("chat actor missing from paged read: %+v", all)
		}
		if _, err := f.Channels.DeleteChannelParticipantReaction(ctx, domain.DeleteChannelParticipantReactionRequest{UserID: f.Member, ChannelID: created.Channel.ID, MessageID: message.Message.ID, Participant: actor}); err != domain.ErrChannelAdminRequired {
			t.Fatalf("member moderation = %v", err)
		}
		deleted, err := f.Channels.DeleteChannelParticipantReaction(ctx, domain.DeleteChannelParticipantReactionRequest{UserID: f.Owner, ChannelID: created.Channel.ID, MessageID: message.Message.ID, Participant: actor})
		if err != nil {
			t.Fatal(err)
		}
		if deleted.ActorChatID != actor.ID || deleted.ActorUserID != 0 || !slices.Equal(deleted.OldReactions, []domain.MessageReaction{thumb}) || len(deleted.Reactions.Results) != 1 || deleted.Reactions.Results[0].Reaction != fire {
			t.Fatalf("chat deletion changed user selection: %+v", deleted)
		}
		if _, err := f.Channels.SetChannelMessageReactions(ctx, req); err != nil {
			t.Fatal(err)
		}
		cleared, err := f.Channels.DeleteChannelParticipantReactions(ctx, domain.DeleteChannelParticipantReactionsRequest{UserID: f.Owner, ChannelID: created.Channel.ID, Participant: actor})
		if err != nil || cleared.Deleted != 1 || len(cleared.Changes) != 1 || cleared.Changes[0].ActorChatID != actor.ID {
			t.Fatalf("chat bulk clear = %+v, %v", cleared, err)
		}
	})
	t.Run("private album targets first surviving message", func(t *testing.T) {
		first, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 23001, Message: "first", GroupedID: 33001, Date: 1700000100})
		if err != nil {
			t.Fatal(err)
		}
		second, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: f.Owner, RecipientUserID: f.Member, RandomID: 23002, Message: "second", GroupedID: 33001, Date: 1700000100})
		if err != nil {
			t.Fatal(err)
		}
		set := domain.SetPrivateMessageReactionsRequest{UserID: f.Member, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.Owner}, MessageID: second.RecipientMessage.ID, Reactions: []domain.MessageReaction{thumb}, Date: 1700000101}
		res, err := f.Messages.SetMessageReactions(ctx, set)
		if err != nil || res.MessageID != first.RecipientMessage.ID {
			t.Fatalf("album first = %+v, %v", res, err)
		}
		if _, err := f.Messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{OwnerUserID: f.Member, IDs: []int{first.RecipientMessage.ID}, Revoke: true, Date: 1700000102}); err != nil {
			t.Fatal(err)
		}
		res, err = f.Messages.SetMessageReactions(ctx, set)
		if err != nil || res.MessageID != second.RecipientMessage.ID {
			t.Fatalf("surviving album = %+v, %v", res, err)
		}
	})
}
