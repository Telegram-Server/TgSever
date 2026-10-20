package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"slices"
	"telesrv/internal/domain"
)

func (r *Router) deliverBotReaction(ctx context.Context, botID int64, kind domain.BotAPIUpdateKind, payload domain.BotMessageReactionUpdated) {
	if slices.Equal(payload.OldReactions, payload.NewReactions) {
		return
	}
	if kind == domain.BotAPIUpdateMessageReaction {
		old, next := make([]tg.ReactionClass, 0, len(payload.OldReactions)), make([]tg.ReactionClass, 0, len(payload.NewReactions))
		for _, reaction := range payload.OldReactions {
			old = append(old, tgMessageReaction(reaction))
		}
		for _, reaction := range payload.NewReactions {
			next = append(next, tgMessageReaction(reaction))
		}
		r.pushUserUpdates(ctx, botID, &tg.Updates{Date: payload.Date, Updates: []tg.UpdateClass{&tg.UpdateBotMessageReaction{Peer: tgPeer(payload.Chat), MsgID: payload.MessageID, Date: payload.Date, Actor: &tg.PeerUser{UserID: payload.ActorUserID}, OldReactions: old, NewReactions: next}}})
	} else {
		counts := make([]tg.ReactionCount, 0, len(payload.Counts))
		for _, count := range payload.Counts {
			counts = append(counts, tg.ReactionCount{Reaction: tgMessageReaction(count.Reaction), Count: count.Count})
		}
		r.pushUserUpdates(ctx, botID, &tg.Updates{Date: payload.Date, Updates: []tg.UpdateClass{&tg.UpdateBotMessageReactions{Peer: tgPeer(payload.Chat), MsgID: payload.MessageID, Date: payload.Date, Reactions: counts}}})
	}
	if r.deps.BotAPIUpdates == nil {
		return
	}
	if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{BotUserID: botID, Kind: kind, Peer: payload.Chat, MessageID: payload.MessageID, Date: payload.Date, Reaction: &payload}); err == nil && created {
		r.notifyBotAPIUpdate(botID)
	}
}

func (r *Router) deliverChannelBotReaction(ctx context.Context, res domain.ChannelMessageReactionsResult, date int) {
	r.deliverChannelBotReactionTo(ctx, res, date, r.botAPIChannelAdminBots(ctx, res.Channel))
}

func (r *Router) deliverChannelBotReactionTo(ctx context.Context, res domain.ChannelMessageReactionsResult, date int, admins []domain.ChannelMember) {
	if len(admins) == 0 {
		return
	}
	payload := domain.BotMessageReactionUpdated{Chat: domain.Peer{Type: domain.PeerTypeChannel, ID: res.Channel.ID}, MessageReactionChange: res.MessageReactionChange, Date: date, Counts: res.Reactions.Results}
	kind := domain.BotAPIUpdateMessageReaction
	if res.Channel.Broadcast && !res.Channel.Megagroup {
		kind = domain.BotAPIUpdateMessageReactionCount
	}
	if kind == domain.BotAPIUpdateMessageReaction && r.userIsBot(ctx, res.ActorUserID) {
		return
	}
	for _, admin := range admins {
		r.deliverBotReaction(ctx, admin.UserID, kind, payload)
	}
}

func (r *Router) deliverPrivateBotReaction(ctx context.Context, actorID int64, res domain.PrivateMessageReactionsResult, date int) {
	if r.userIsBot(ctx, actorID) {
		return
	}
	for _, msg := range res.Messages {
		if msg.OwnerUserID == actorID || msg.OwnerUserID == 0 || !r.userIsBot(ctx, msg.OwnerUserID) {
			continue
		}
		change := res.MessageReactionChange
		change.MessageID = msg.ID
		r.deliverBotReaction(ctx, msg.OwnerUserID, domain.BotAPIUpdateMessageReaction, domain.BotMessageReactionUpdated{Chat: domain.Peer{Type: domain.PeerTypeUser, ID: actorID}, MessageReactionChange: change, Date: date})
	}
}

func (r *Router) validatePrivateReactionSelection(ctx context.Context, userID int64, peer domain.Peer, id int, reactions []domain.MessageReaction) error {
	if peer.ID == userID {
		return nil
	}
	res, err := r.deps.Messages.GetMessageReactions(ctx, userID, domain.PrivateMessageReactionsRequest{OwnerUserID: userID, Peer: peer, IDs: []int{id}})
	if err != nil {
		return messageReactionErr(err)
	}
	for _, msg := range res.Messages {
		if msg.OwnerUserID == userID && msg.ID == id && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindService {
			return messageIDInvalidErr()
		}
	}
	for _, reaction := range reactions {
		if reaction.Type != domain.MessageReactionCustomEmoji {
			if err := r.validateDefaultReaction(ctx, reaction); err != nil {
				return err
			}
			continue
		}
		if !r.userIsBot(ctx, userID) && r.viewerPremium(ctx, userID) {
			continue
		}
		present := false
		for _, count := range res.Reactions.Results {
			if count.Count > 0 && count.Reaction.Key() == reaction.Key() {
				present = true
				break
			}
		}
		if !present {
			return reactionInvalidErr()
		}
	}
	return nil
}
