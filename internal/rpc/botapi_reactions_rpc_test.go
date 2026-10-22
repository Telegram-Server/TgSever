package rpc

import (
	"context"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"slices"
	"strings"
	"telesrv/internal/domain"
	"testing"
)

func TestDeleteParticipantReactionsRejectsPrivatePeer(t *testing.T) {
	f := newReactionPolicyFixture(t, false)
	ctx := WithUserID(context.Background(), f.ownerID)
	peer := &tg.InputPeerUser{UserID: f.memberID, AccessHash: 22}
	participant := &tg.InputPeerUser{UserID: f.member2ID, AccessHash: 33}
	if _, err := f.router.onMessagesDeleteParticipantReactions(ctx, &tg.MessagesDeleteParticipantReactionsRequest{Peer: peer, Participant: participant}); err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("delete private participant reactions = %v, want PEER_ID_INVALID", err)
	}
	if _, err := f.router.onMessagesDeleteParticipantReaction(ctx, &tg.MessagesDeleteParticipantReactionRequest{Peer: peer, Participant: participant, MsgID: 1}); err == nil || !strings.Contains(err.Error(), "PEER_ID_INVALID") {
		t.Fatalf("delete private participant reaction = %v, want PEER_ID_INVALID", err)
	}
}

func reactionBotEvents(t *testing.T, f botAPIBridgeFixture) []domain.UpdateEvent {
	t.Helper()
	events, err := f.router.BotAPIUpdates(f.ctx, f.bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]domain.UpdateEvent, 0)
	for _, event := range events {
		if event.Type == domain.UpdateEventBotMessageReaction || event.Type == domain.UpdateEventBotMessageReactionCount {
			out = append(out, event)
		}
	}
	return out
}

func sendBridgeReaction(t *testing.T, f botAPIBridgeFixture, actor int64, peer tg.InputPeerClass, id int, reactions ...tg.ReactionClass) {
	t.Helper()
	req := &tg.MessagesSendReactionRequest{Peer: peer, MsgID: id, Big: true}
	req.SetReaction(reactions)
	req.SetAddToRecent(true)
	if _, err := f.router.onMessagesSendReaction(WithUserID(f.ctx, actor), req); err != nil {
		t.Fatal(err)
	}
}

func subscribeReactionBot(t *testing.T, f botAPIBridgeFixture) {
	t.Helper()
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateMessageReaction, domain.BotAPIUpdateMessageReactionCount}); err != nil {
		t.Fatal(err)
	}
}

func TestBotAPIReactionPrivateOldNewAndBig(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sessions := &reactionSessionRecorder{captureSessions: f.sessions, botID: f.bot.ID}
	f.router.deps.Sessions = sessions
	subscribeReactionBot(t, f)
	sent, err := f.messages.SendPrivateText(f.ctx, f.owner.ID, domain.SendPrivateTextRequest{RecipientUserID: f.bot.ID, RandomID: 71001, Message: "react", Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	peer := &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}
	sendBridgeReaction(t, f, f.owner.ID, peer, sent.SenderMessage.ID, &tg.ReactionEmoji{Emoticon: "👍"})
	sendBridgeReaction(t, f, f.owner.ID, peer, sent.SenderMessage.ID, &tg.ReactionEmoji{Emoticon: "❤️"})
	events := reactionBotEvents(t, f)
	if len(events) != 2 {
		t.Fatalf("private reactions=%+v", events)
	}
	thumb := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}
	heart := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "❤️"}
	first, second := events[0].BotReaction, events[1].BotReaction
	if first.MessageID != sent.RecipientMessage.ID || first.ActorUserID != f.owner.ID || first.Chat != (domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}) || len(first.OldReactions) != 0 || !slices.Equal(first.NewReactions, []domain.MessageReaction{thumb}) || !slices.Equal(second.OldReactions, []domain.MessageReaction{thumb}) || !slices.Equal(second.NewReactions, []domain.MessageReaction{heart}) {
		t.Fatalf("old/new=%+v %+v", first, second)
	}
	stored, err := f.messages.GetMessageReactions(f.ctx, f.owner.ID, domain.PrivateMessageReactionsRequest{OwnerUserID: f.owner.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, IDs: []int{sent.SenderMessage.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Reactions.Recent) != 1 || !stored.Reactions.Recent[0].Big {
		t.Fatalf("big reaction=%+v", stored.Reactions)
	}
	if len(sessions.reactions) != 2 {
		t.Fatalf("MTProto bot reactions=%+v", sessions.reactions)
	}
	wire := sessions.reactions[1].(*tg.UpdateBotMessageReaction)
	if wire.MsgID != sent.RecipientMessage.ID || wire.Actor.(*tg.PeerUser).UserID != f.owner.ID || wire.OldReactions[0].(*tg.ReactionEmoji).Emoticon != "👍" || wire.NewReactions[0].(*tg.ReactionEmoji).Emoticon != "❤️" {
		t.Fatalf("MTProto reaction=%+v", wire)
	}
}

func TestBotAPIReactionGroupAdminAndModeration(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	subscribeReactionBot(t, f)
	id := f.sendAs(t, f.owner.ID, "react", 71002)
	peer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
	sendBridgeReaction(t, f, f.member.ID, peer, id, &tg.ReactionEmoji{Emoticon: "👍"})
	if got := reactionBotEvents(t, f); len(got) != 0 {
		t.Fatalf("non-admin bot received=%+v", got)
	}
	f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
	sendBridgeReaction(t, f, f.member.ID, peer, id, &tg.ReactionEmoji{Emoticon: "🔥"})
	participant := &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}
	if _, err := f.router.onMessagesDeleteParticipantReaction(WithUserID(f.ctx, f.bot.ID), &tg.MessagesDeleteParticipantReactionRequest{Peer: peer, MsgID: id, Participant: participant}); err != nil {
		t.Fatal(err)
	}
	sendBridgeReaction(t, f, f.member.ID, peer, id, &tg.ReactionEmoji{Emoticon: "👍"})
	if _, err := f.router.onMessagesDeleteParticipantReactions(WithUserID(f.ctx, f.bot.ID), &tg.MessagesDeleteParticipantReactionsRequest{Peer: peer, Participant: participant}); err != nil {
		t.Fatal(err)
	}
	events := reactionBotEvents(t, f)
	if len(events) != 4 {
		t.Fatalf("group reactions=%+v", events)
	}
	for _, index := range []int{1, 3} {
		u := events[index].BotReaction
		if events[index].Type != domain.UpdateEventBotMessageReaction || u.ActorUserID != f.member.ID || len(u.OldReactions) != 1 || len(u.NewReactions) != 0 {
			t.Fatalf("moderation event=%+v", events[index])
		}
	}
	sendBridgeReaction(t, f, f.bot.ID, peer, id, &tg.ReactionEmoji{Emoticon: "👍"})
	if got := reactionBotEvents(t, f); len(got) != 4 {
		t.Fatalf("bot-generated reaction event=%+v", got)
	}
	recent, err := f.channels.RecentReactions(f.ctx, f.bot.ID, 100)
	if err != nil || len(recent) != 0 {
		t.Fatalf("channel bot recent=%+v err=%v", recent, err)
	}
}

func TestBotAPIReactionAnonymousChannelCounts(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	sessions := &reactionSessionRecorder{captureSessions: f.sessions, botID: f.bot.ID}
	f.router.deps.Sessions = sessions
	subscribeReactionBot(t, f)
	created, err := f.channels.CreateChannel(f.ctx, f.owner.ID, domain.CreateChannelRequest{Title: "Anonymous", Broadcast: true, MemberUserIDs: []int64{f.bot.ID, f.member.ID}, Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	f.channel = created.Channel
	f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
	sent, err := f.channels.SendMessage(f.ctx, f.owner.ID, domain.SendChannelMessageRequest{ChannelID: f.channel.ID, RandomID: 71003, Message: "news", Date: 1700000001})
	if err != nil {
		t.Fatal(err)
	}
	peer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
	sendBridgeReaction(t, f, f.member.ID, peer, sent.Message.ID, &tg.ReactionEmoji{Emoticon: "👍"})
	if _, err := f.router.onMessagesDeleteParticipantReaction(WithUserID(f.ctx, f.owner.ID), &tg.MessagesDeleteParticipantReactionRequest{Peer: peer, MsgID: sent.Message.ID, Participant: &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}}); err != nil {
		t.Fatal(err)
	}
	events := reactionBotEvents(t, f)
	if len(events) != 2 || events[0].Type != domain.UpdateEventBotMessageReactionCount || len(events[0].BotReaction.Counts) != 1 || events[0].BotReaction.Counts[0].Count != 1 || events[1].Type != domain.UpdateEventBotMessageReactionCount || len(events[1].BotReaction.Counts) != 0 {
		t.Fatalf("anonymous counts=%+v", events)
	}
	if len(sessions.reactions) != 2 {
		t.Fatalf("anonymous MTProto updates=%+v", sessions.reactions)
	}
	if update, ok := sessions.reactions[0].(*tg.UpdateBotMessageReactions); !ok || len(update.Reactions) != 1 || update.Reactions[0].Count != 1 {
		t.Fatalf("anonymous MTProto count=%+v", sessions.reactions[0])
	}
}

func TestBotAPIReactionBulkClearsBeyondOnePage(t *testing.T) {
	for _, size := range []struct {
		name             string
		count, remaining int
	}{{"beyond_page", 1001, 0}, {"cap_10000", 10001, 1}} {
		t.Run(size.name, func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			created, err := f.channels.CreateChannel(f.ctx, f.owner.ID, domain.CreateChannelRequest{
				Title: "Bulk anonymous reactions", Broadcast: true, MemberUserIDs: []int64{f.bot.ID, f.member.ID}, Date: 1700000000,
			})
			if err != nil {
				t.Fatal(err)
			}
			f.channel = created.Channel
			f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
			peer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
			// Seed messages before reactions. Anonymous reactions have no unread
			// bookkeeping, and sends must not rescan a growing reaction history.
			ids := make([]int, 0, size.count)
			for i := range size.count {
				sent, err := f.channels.SendMessage(f.ctx, f.owner.ID, domain.SendChannelMessageRequest{ChannelID: f.channel.ID, RandomID: int64(72000 + i), Message: "react", Date: 1700000000 + i})
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, sent.Message.ID)
			}
			for i, id := range ids {
				if _, err := f.channels.SetMessageReactions(f.ctx, f.member.ID, domain.SetChannelMessageReactionsRequest{ChannelID: f.channel.ID, MessageID: id, Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "👍"}}, Date: 1700000001 + i}); err != nil {
					t.Fatal(err)
				}
			}
			subscribeReactionBot(t, f)
			if _, err := f.router.onMessagesDeleteParticipantReactions(WithUserID(f.ctx, f.bot.ID), &tg.MessagesDeleteParticipantReactionsRequest{Peer: peer, Participant: &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}}); err != nil {
				t.Fatal(err)
			}
			// Inspect remaining state directly: all through 10000, never more.
			remaining, err := f.channels.DeleteParticipantReactions(f.ctx, f.owner.ID, domain.DeleteChannelParticipantReactionsRequest{ChannelID: f.channel.ID, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}})
			if err != nil || remaining.Deleted != size.remaining {
				t.Fatalf("remaining=%+v err=%v, want %d", remaining, err, size.remaining)
			}
			// Membership events may precede the reaction subscription. Consume
			// by update kind and cursor, not the incidental first queue row.
			count := 0
			for from := int64(1); ; {
				rows, err := f.router.deps.BotAPIUpdates.ListBotAPIUpdates(f.ctx, f.bot.ID, from, 100)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) == 0 {
					break
				}
				for _, row := range rows {
					if row.Kind != domain.BotAPIUpdateMessageReactionCount {
						continue
					}
					if row.Reaction == nil || row.Reaction.ActorUserID != f.member.ID || len(row.Reaction.OldReactions) != 1 || row.Reaction.OldReactions[0].Emoticon != "👍" || len(row.Reaction.NewReactions) != 0 {
						t.Fatalf("bulk reaction update=%+v", row)
					}
					count++
				}
				from = rows[len(rows)-1].ID + 1
			}
			if count != min(size.count, 10000) {
				t.Fatalf("bulk reaction updates=%d, want %d", count, min(size.count, 10000))
			}
		})
	}
}

type reactionPremiumUsers struct {
	UsersService
	premium map[int64]bool
}

func (s reactionPremiumUsers) PremiumActive(_ context.Context, id int64) bool { return s.premium[id] }

func TestPrivateReactionEligibilityAndBotRecent(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	premium := map[int64]bool{}
	f.router.deps.Users = reactionPremiumUsers{UsersService: f.router.deps.Users, premium: premium}
	sent, err := f.messages.SendPrivateText(f.ctx, f.owner.ID, domain.SendPrivateTextRequest{RecipientUserID: f.bot.ID, RandomID: 73001, Message: "custom", Date: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	botPeer := &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}
	ownerPeer := &tg.InputPeerUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash}
	custom := &tg.ReactionCustomEmoji{DocumentID: 9000001}
	reject := func(actor int64, peer tg.InputPeerClass, id int, reaction tg.ReactionClass, code string) {
		t.Helper()
		req := &tg.MessagesSendReactionRequest{Peer: peer, MsgID: id}
		req.SetReaction([]tg.ReactionClass{reaction})
		if _, err := f.router.onMessagesSendReaction(WithUserID(f.ctx, actor), req); err == nil || !strings.Contains(err.Error(), code) {
			t.Fatalf("actor %d reaction err=%v, want %s", actor, err, code)
		}
	}
	reject(f.owner.ID, botPeer, sent.SenderMessage.ID, custom, "REACTION_INVALID")
	// Premium bots still cannot introduce a custom emoji.
	premium[f.bot.ID] = true
	reject(f.bot.ID, ownerPeer, sent.RecipientMessage.ID, custom, "REACTION_INVALID")
	premium[f.owner.ID] = true
	sendBridgeReaction(t, f, f.owner.ID, botPeer, sent.SenderMessage.ID, custom)
	// An existing custom emoji is available to a bot and to a non-premium user.
	sendBridgeReaction(t, f, f.bot.ID, ownerPeer, sent.RecipientMessage.ID, custom)
	premium[f.owner.ID] = false
	sendBridgeReaction(t, f, f.owner.ID, botPeer, sent.SenderMessage.ID, custom)
	recent, err := f.channels.RecentReactions(f.ctx, f.bot.ID, 100)
	if err != nil || len(recent) != 0 {
		t.Fatalf("bot recent=%+v err=%v", recent, err)
	}
	service, err := f.messages.PostPrivateServiceMessage(f.ctx, f.owner.ID, f.bot.ID, domain.SendPrivateTextRequest{RandomID: 73002, Date: 1700000001, Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionHistoryClear}}})
	if err != nil {
		t.Fatal(err)
	}
	reject(f.owner.ID, botPeer, service.RecipientMessage.ID, &tg.ReactionEmoji{Emoticon: "👍"}, "MESSAGE_ID_INVALID")
}

func TestBotAPIReactionRequiresExplicitAllowedUpdates(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
	id := f.sendAs(t, f.owner.ID, "react", 74001)
	peer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
	sendBridgeReaction(t, f, f.member.ID, peer, id, &tg.ReactionEmoji{Emoticon: "👍"})
	if events := reactionBotEvents(t, f); len(events) != 0 {
		t.Fatalf("default reaction subscription=%+v", events)
	}
	subscribeReactionBot(t, f)
	sendBridgeReaction(t, f, f.member.ID, peer, id, &tg.ReactionEmoji{Emoticon: "🔥"})
	events := reactionBotEvents(t, f)
	if len(events) != 1 || events[0].BotReaction.OldReactions[0].Emoticon != "👍" {
		t.Fatalf("opted-in update=%+v", events)
	}
}

type reactionSessionRecorder struct {
	*captureSessions
	botID     int64
	reactions []tg.UpdateClass
}

func (s *reactionSessionRecorder) PushToUserExceptAuthKeySession(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64, kind proto.MessageType, box tg.UpdatesClass) (int, error) {
	if userID == s.botID {
		if updates, ok := box.(*tg.Updates); ok {
			for _, update := range updates.Updates {
				switch update.(type) {
				case *tg.UpdateBotMessageReaction, *tg.UpdateBotMessageReactions:
					s.reactions = append(s.reactions, update)
				}
			}
		}
	}
	return s.captureSessions.PushToUserExceptAuthKeySession(ctx, userID, authKeyID, sessionID, kind, box)
}
