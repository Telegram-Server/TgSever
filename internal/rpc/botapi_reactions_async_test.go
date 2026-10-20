package rpc

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"

	appchannels "telesrv/internal/app/channels"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type reactionDeliveryGate struct {
	entered chan struct{}
	release chan struct{}
	enter   sync.Once
	leave   sync.Once
}

func newReactionDeliveryGate() *reactionDeliveryGate {
	return &reactionDeliveryGate{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *reactionDeliveryGate) wait(ctx context.Context) error {
	g.enter.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *reactionDeliveryGate) unblock() { g.leave.Do(func() { close(g.release) }) }

type delayedReactionChannels struct {
	*appchannels.Service
	gate  *reactionDeliveryGate
	stage string
	botID int64
}

func (s delayedReactionChannels) ActiveBotMemberIDs(ctx context.Context, viewerID, channelID int64, limit int) ([]int64, error) {
	if s.stage == "candidates" {
		if err := s.gate.wait(ctx); err != nil {
			return nil, err
		}
	}
	return s.Service.ActiveBotMemberIDs(ctx, viewerID, channelID, limit)
}

func (s delayedReactionChannels) GetParticipant(ctx context.Context, viewerID, channelID, participantID int64) (domain.ChannelMember, error) {
	if s.stage == "roles" && viewerID == s.botID && participantID == s.botID {
		if err := s.gate.wait(ctx); err != nil {
			return domain.ChannelMember{}, err
		}
	}
	return s.Service.GetParticipant(ctx, viewerID, channelID, participantID)
}

type delayedReactionUpdateStore struct {
	store.BotAPIUpdateStore
	gate *reactionDeliveryGate
}

func (s delayedReactionUpdateStore) EnqueueBotAPIUpdate(ctx context.Context, req domain.EnqueueBotAPIUpdateRequest) (domain.BotAPIUpdate, bool, error) {
	if req.Kind == domain.BotAPIUpdateMessageReaction || req.Kind == domain.BotAPIUpdateMessageReactionCount {
		if err := s.gate.wait(ctx); err != nil {
			return domain.BotAPIUpdate{}, false, err
		}
	}
	return s.BotAPIUpdateStore.EnqueueBotAPIUpdate(ctx, req)
}

func runReactionRPCUnblocked(t *testing.T, run func() error) {
	t.Helper()
	finished := make(chan error, 1)
	go func() { finished <- run() }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reaction RPC waited for blocked bot delivery storage")
	}
}

func TestBotAPIReactionAsyncRPCAndModerationSnapshots(t *testing.T) {
	for _, broadcast := range []bool{false, true} {
		for _, stage := range []string{"candidates", "roles", "insert"} {
			name := "group/" + stage
			if broadcast {
				name = "broadcast/" + stage
			}
			t.Run(name, func(t *testing.T) {
				f := newBotAPIBridgeFixture(t)
				if broadcast {
					created, err := f.channels.CreateChannel(f.ctx, f.owner.ID, domain.CreateChannelRequest{
						Title: "Async counts", Broadcast: true, MemberUserIDs: []int64{f.bot.ID, f.member.ID}, Date: 1700000000,
					})
					if err != nil {
						t.Fatal(err)
					}
					f.channel = created.Channel
				}
				f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
				subscribeReactionBot(t, f)
				ids := make([]int, 2)
				for i := range 2 {
					sent, err := f.channels.SendMessage(f.ctx, f.owner.ID, domain.SendChannelMessageRequest{
						ChannelID: f.channel.ID, RandomID: int64(75000 + i), Message: "async reaction", Date: 1700000001,
					})
					if err != nil {
						t.Fatal(err)
					}
					ids[i] = sent.Message.ID
				}
				gate := newReactionDeliveryGate()
				f.router.deps.Channels = delayedReactionChannels{Service: f.channels, gate: gate, stage: stage, botID: f.bot.ID}
				if stage == "insert" {
					f.router.deps.BotAPIUpdates = delayedReactionUpdateStore{BotAPIUpdateStore: f.router.deps.BotAPIUpdates, gate: gate}
				}
				cancelWorker := startBotAPIEnqueueTestWorker(t, f.router.botAPIEnqueueQueue)
				t.Cleanup(gate.unblock)
				peer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
				participant := &tg.InputPeerUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}
				react := func(id int, emoji string) {
					t.Helper()
					runReactionRPCUnblocked(t, func() error {
						req := &tg.MessagesSendReactionRequest{Peer: peer, MsgID: id}
						req.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: emoji}})
						_, err := f.router.onMessagesSendReaction(WithUserID(f.ctx, f.member.ID), req)
						return err
					})
				}
				react(ids[0], "👍")
				awaitBotAPIEnqueueTestSignal(t, gate.entered)
				react(ids[0], "🔥")
				runReactionRPCUnblocked(t, func() error {
					_, err := f.router.onMessagesDeleteParticipantReaction(WithUserID(f.ctx, f.owner.ID), &tg.MessagesDeleteParticipantReactionRequest{
						Peer: peer, MsgID: ids[0], Participant: participant,
					})
					return err
				})
				react(ids[0], "👍")
				react(ids[1], "👍")
				runReactionRPCUnblocked(t, func() error {
					_, err := f.router.onMessagesDeleteParticipantReactions(WithUserID(f.ctx, f.owner.ID), &tg.MessagesDeleteParticipantReactionsRequest{
						Peer: peer, Participant: participant,
					})
					return err
				})
				// Drain with a canceled worker context. Pending snapshots must still
				// reach real getUpdates storage, including both moderation paths.
				cancelWorker()
				gate.unblock()
				awaitBotAPIEnqueueTestSignal(t, f.router.botAPIEnqueueQueue.done)
				events := reactionBotEvents(t, f)
				if len(events) != 7 {
					t.Fatalf("reaction events = %+v, want seven committed transitions", events)
				}
				selection := func(emoji string) []domain.MessageReaction {
					if emoji == "" {
						return nil
					}
					return []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: emoji}}
				}
				old := []string{"", "👍", "🔥", "", "", "👍", "👍"}
				next := []string{"👍", "🔥", "", "👍", "👍", "", ""}
				cleared := make(map[int]bool)
				for i, event := range events {
					payload := event.BotReaction
					if payload == nil || !slices.Equal(payload.OldReactions, selection(old[i])) || !slices.Equal(payload.NewReactions, selection(next[i])) {
						t.Fatalf("event %d = %+v, want %q -> %q", i, payload, old[i], next[i])
					}
					if payload.ActorUserID != f.member.ID || payload.Chat.ID != f.channel.ID {
						t.Fatalf("event %d actor/chat = %+v", i, payload)
					}
					if i < 5 {
						wantID := ids[0]
						if i == 4 {
							wantID = ids[1]
						}
						if payload.MessageID != wantID {
							t.Fatalf("event %d message = %d, want %d", i, payload.MessageID, wantID)
						}
					} else {
						cleared[payload.MessageID] = true
					}
					if broadcast {
						if event.Type != domain.UpdateEventBotMessageReactionCount {
							t.Fatalf("anonymous event exposed actor update: %+v", event)
						}
						if next[i] == "" {
							if len(payload.Counts) != 0 {
								t.Fatalf("clear counts = %+v", payload.Counts)
							}
						} else if len(payload.Counts) != 1 || payload.Counts[0].Count != 1 || payload.Counts[0].Reaction.Emoticon != next[i] {
							t.Fatalf("committed counts = %+v, want one %q", payload.Counts, next[i])
						}
					} else if event.Type != domain.UpdateEventBotMessageReaction {
						t.Fatalf("group event kind = %s", event.Type)
					}
				}
				if !cleared[ids[0]] || !cleared[ids[1]] {
					t.Fatalf("bulk clear messages = %v, want %v", cleared, ids)
				}
			})
		}
	}
}

func TestBotAPIReactionPrivateAsyncOldNew(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	subscribeReactionBot(t, f)
	sent, err := f.messages.SendPrivateText(f.ctx, f.owner.ID, domain.SendPrivateTextRequest{
		RecipientUserID: f.bot.ID, RandomID: 76001, Message: "async private reaction", Date: 1700000000,
	})
	if err != nil {
		t.Fatal(err)
	}
	gate := newReactionDeliveryGate()
	f.router.deps.BotAPIUpdates = delayedReactionUpdateStore{BotAPIUpdateStore: f.router.deps.BotAPIUpdates, gate: gate}
	cancel := startBotAPIEnqueueTestWorker(t, f.router.botAPIEnqueueQueue)
	t.Cleanup(gate.unblock)
	for i, emoji := range []string{"👍", "❤️"} {
		runReactionRPCUnblocked(t, func() error {
			req := &tg.MessagesSendReactionRequest{
				Peer: &tg.InputPeerUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}, MsgID: sent.SenderMessage.ID,
			}
			req.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: emoji}})
			_, err := f.router.onMessagesSendReaction(WithUserID(f.ctx, f.owner.ID), req)
			return err
		})
		if i == 0 {
			awaitBotAPIEnqueueTestSignal(t, gate.entered)
		}
	}
	cancel()
	gate.unblock()
	awaitBotAPIEnqueueTestSignal(t, f.router.botAPIEnqueueQueue.done)
	events := reactionBotEvents(t, f)
	thumb := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "👍"}
	heart := domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: "❤️"}
	if len(events) != 2 {
		t.Fatalf("private transitions = %+v", events)
	}
	for i, event := range events {
		if event.BotReaction == nil || event.BotReaction.MessageID != sent.RecipientMessage.ID ||
			event.BotReaction.ActorUserID != f.owner.ID || event.BotReaction.Chat != (domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}) {
			t.Fatalf("private event %d = %+v", i, event)
		}
	}
	first, second := events[0].BotReaction, events[1].BotReaction
	if len(first.OldReactions) != 0 || !slices.Equal(first.NewReactions, []domain.MessageReaction{thumb}) ||
		!slices.Equal(second.OldReactions, []domain.MessageReaction{thumb}) || !slices.Equal(second.NewReactions, []domain.MessageReaction{heart}) {
		t.Fatalf("private snapshots = %+v, %+v", first, second)
	}
}

func TestBotAPIReactionAsyncHonorsCurrentVisibilityAndAllowedUpdates(t *testing.T) {
	for _, change := range []string{"demote", "unsubscribe"} {
		t.Run(change, func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
			subscribeReactionBot(t, f)
			id := f.sendAs(t, f.owner.ID, "visibility", 77001)
			gate := newReactionDeliveryGate()
			f.router.deps.Channels = delayedReactionChannels{Service: f.channels, gate: gate, stage: "candidates", botID: f.bot.ID}
			cancel := startBotAPIEnqueueTestWorker(t, f.router.botAPIEnqueueQueue)
			t.Cleanup(gate.unblock)
			runReactionRPCUnblocked(t, func() error {
				req := &tg.MessagesSendReactionRequest{
					Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}, MsgID: id,
				}
				req.SetReaction([]tg.ReactionClass{&tg.ReactionEmoji{Emoticon: "👍"}})
				_, err := f.router.onMessagesSendReaction(WithUserID(f.ctx, f.member.ID), req)
				return err
			})
			awaitBotAPIEnqueueTestSignal(t, gate.entered)
			if change == "demote" {
				_, err := f.router.onChannelsEditAdmin(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsEditAdminRequest{
					Channel: f.inputChannel(),
					UserID:  &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
				})
				if err != nil {
					t.Fatal(err)
				}
				member, err := f.channels.GetParticipant(f.ctx, f.bot.ID, f.channel.ID, f.bot.ID)
				if err != nil || member.Role != domain.ChannelRoleMember {
					t.Fatalf("bot was not demoted: %+v, %v", member, err)
				}
			} else if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateMessage}); err != nil {
				t.Fatal(err)
			}
			cancel()
			gate.unblock()
			awaitBotAPIEnqueueTestSignal(t, f.router.botAPIEnqueueQueue.done)
			if events := reactionBotEvents(t, f); len(events) != 0 {
				t.Fatalf("reaction delivered after %s: %+v", change, events)
			}
		})
	}
}

func TestBotAPIReactionQueuedSnapshotIsImmutable(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
	subscribeReactionBot(t, f)
	id := f.sendAs(t, f.owner.ID, "immutable snapshot", 78001)
	for _, emoji := range []string{"👍", "🔥"} {
		res, err := f.channels.SetMessageReactions(f.ctx, f.member.ID, domain.SetChannelMessageReactionsRequest{
			ChannelID: f.channel.ID, MessageID: id,
			Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: emoji}}, Date: 1700000001,
		})
		if err != nil {
			t.Fatal(err)
		}
		if emoji == "👍" {
			continue
		}
		gate := newReactionDeliveryGate()
		cancel := startBotAPIEnqueueTestWorker(t, f.router.botAPIEnqueueQueue)
		t.Cleanup(gate.unblock)
		f.router.botAPIEnqueueQueue.Enqueue(f.ctx, func(ctx context.Context) { _ = gate.wait(ctx) })
		awaitBotAPIEnqueueTestSignal(t, gate.entered)
		f.router.enqueueChannelBotReaction(f.ctx, res, 1700000001)
		// Reuse all source backing arrays after admission. Consumers must see
		// the committed transition, not these later caller-side mutations.
		res.OldReactions[0].Emoticon = "❤️"
		res.NewReactions[0].Emoticon = "❤️"
		res.Reactions.Results[0].Count = 99
		res.Reactions.Results[0].Reaction.Emoticon = "❤️"
		cancel()
		gate.unblock()
		awaitBotAPIEnqueueTestSignal(t, f.router.botAPIEnqueueQueue.done)
		events := reactionBotEvents(t, f)
		if len(events) != 1 {
			t.Fatalf("snapshot events = %+v", events)
		}
		payload := events[0].BotReaction
		if payload == nil || len(payload.OldReactions) != 1 || payload.OldReactions[0].Emoticon != "👍" ||
			len(payload.NewReactions) != 1 || payload.NewReactions[0].Emoticon != "🔥" ||
			len(payload.Counts) != 1 || payload.Counts[0].Count != 1 || payload.Counts[0].Reaction.Emoticon != "🔥" {
			t.Fatalf("queued snapshot changed: %+v", payload)
		}
	}
}
