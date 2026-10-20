package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
)

// Tags are current membership metadata, not immutable message author snapshots.
// Resolve them at the same response boundaries as the other peer read models.
func (r *Router) applyChannelMessageRanks(ctx context.Context, viewerID int64, messages []tg.MessageClass) {
	if r.deps.Channels == nil {
		return
	}
	type key struct{ channel, user int64 }
	var ranks map[key]string
	for _, value := range messages {
		message, ok := value.(*tg.Message)
		if !ok || message.Post {
			continue
		}
		channel, ok := message.PeerID.(*tg.PeerChannel)
		if !ok {
			continue
		}
		user, ok := message.FromID.(*tg.PeerUser)
		if !ok {
			continue
		}
		k := key{channel.ChannelID, user.UserID}
		rank, cached := ranks[k]
		if !cached {
			if ranks == nil && len(messages) > 1 {
				ranks = make(map[key]string)
			}
			if member, err := r.deps.Channels.GetParticipant(ctx, viewerID, channel.ChannelID, user.UserID); err == nil {
				rank = member.Rank
			}
			if ranks != nil {
				ranks[k] = rank
			}
		}
		if rank != "" {
			message.SetFromRank(rank)
		}
	}
}

func (r *Router) applyChannelUpdateRanks(ctx context.Context, viewerID int64, updates []tg.UpdateClass) {
	for _, update := range updates {
		switch u := update.(type) {
		case *tg.UpdateNewChannelMessage:
			r.applyChannelMessageRanks(ctx, viewerID, []tg.MessageClass{u.Message})
		case *tg.UpdateEditChannelMessage:
			r.applyChannelMessageRanks(ctx, viewerID, []tg.MessageClass{u.Message})
		}
	}
}
