package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// tgBotMessageReactionUpdate 把单人反应变化记录（#150）投影成 updateBotMessageReaction，附带
// 反应者与（超级群/频道时）min 形态的频道。
func tgBotMessageReactionUpdate(update domain.BotUpdate) (tg.UpdateClass, []int64, tg.ChatClass) {
	r := update.MessageReaction
	out := &tg.UpdateBotMessageReaction{
		Peer:         tgPeer(r.Peer),
		MsgID:        r.MessageID,
		Date:         update.Date,
		Actor:        tgPeer(r.Actor),
		OldReactions: tgReactionList(r.Old),
		NewReactions: tgReactionList(r.New),
		Qts:          update.Qts,
	}
	var userIDs []int64
	if r.Actor.Type == domain.PeerTypeUser {
		userIDs = append(userIDs, r.Actor.ID)
	}
	if r.Peer.Type == domain.PeerTypeUser {
		userIDs = append(userIDs, r.Peer.ID)
	}
	return out, userIDs, botReactionChat(update.BotUserID, r.Peer, r.Channel)
}

// tgBotMessageReactionsUpdate 把匿名反应计数记录（#150）投影成 updateBotMessageReactions。
func tgBotMessageReactionsUpdate(update domain.BotUpdate) (tg.UpdateClass, []int64, tg.ChatClass) {
	r := update.MessageReactions
	counts := make([]tg.ReactionCount, 0, len(r.Reactions))
	for _, item := range r.Reactions {
		if reaction := tgMessageReaction(item.Reaction); reaction != nil && item.Count > 0 {
			counts = append(counts, tg.ReactionCount{Reaction: reaction, Count: item.Count})
		}
	}
	return &tg.UpdateBotMessageReactions{
		Peer:      tgPeer(r.Peer),
		MsgID:     r.MessageID,
		Date:      update.Date,
		Reactions: counts,
		Qts:       update.Qts,
	}, nil, botReactionChat(update.BotUserID, r.Peer, r.Channel)
}

func botReactionChat(botUserID int64, peer domain.Peer, channel domain.Channel) tg.ChatClass {
	if peer.Type != domain.PeerTypeChannel || channel.ID != peer.ID {
		return nil
	}
	return tgChannelChatMin(botUserID, channel)
}

func tgReactionList(in []domain.MessageReaction) []tg.ReactionClass {
	out := make([]tg.ReactionClass, 0, len(in))
	for _, reaction := range in {
		if converted := tgMessageReaction(reaction); converted != nil {
			out = append(out, converted)
		}
	}
	return out
}
