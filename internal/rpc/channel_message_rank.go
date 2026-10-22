package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Tags are current membership metadata, not immutable message author snapshots.
// Call only after message visibility is authorized. Nothing is retained across
// responses; fanout alone shares the same metadata snapshot across its viewers.
func (r *Router) applyChannelMessageRanks(ctx context.Context, _ int64, messages []tg.MessageClass) {
	var ranks map[domain.ChannelMessageAuthor]string
	r.applyChannelMessageRanksWithCache(ctx, messages, &ranks)
}

func (r *Router) applyChannelMessageRanksWithCache(ctx context.Context, messages []tg.MessageClass, ranks *map[domain.ChannelMessageAuthor]string) {
	if r.deps.Channels == nil {
		return
	}
	var missing []domain.ChannelMessageAuthor
	for _, value := range messages {
		author, ok := channelMessageAuthor(value)
		if !ok {
			continue
		}
		if _, loaded := (*ranks)[author]; loaded {
			continue
		}
		if *ranks == nil {
			*ranks = make(map[domain.ChannelMessageAuthor]string, len(messages))
		}
		(*ranks)[author] = ""
		missing = append(missing, author)
	}
	if len(missing) > 0 {
		loaded, err := r.deps.Channels.GetMessageRanks(ctx, missing)
		if err != nil {
			// Failed reads are not negative cache entries for subsequent viewers.
			for _, author := range missing {
				delete(*ranks, author)
			}
		} else {
			for _, author := range missing {
				(*ranks)[author] = loaded[author]
			}
		}
	}
	for _, value := range messages {
		author, ok := channelMessageAuthor(value)
		if !ok {
			continue
		}
		message := value.(*tg.Message)
		message.FromRank = ""
		message.Flags2.Unset(12)
		if rank := (*ranks)[author]; rank != "" {
			message.SetFromRank(rank)
		}
	}
}

func channelMessageAuthor(value tg.MessageClass) (domain.ChannelMessageAuthor, bool) {
	message, ok := value.(*tg.Message)
	if !ok || message == nil || message.Post {
		return domain.ChannelMessageAuthor{}, false
	}
	channel, ok := message.PeerID.(*tg.PeerChannel)
	if !ok || channel == nil || channel.ChannelID == 0 {
		return domain.ChannelMessageAuthor{}, false
	}
	user, ok := message.FromID.(*tg.PeerUser)
	if !ok || user == nil || user.UserID == 0 {
		return domain.ChannelMessageAuthor{}, false
	}
	return domain.ChannelMessageAuthor{ChannelID: channel.ChannelID, UserID: user.UserID}, true
}

func channelRankMessages(messages []tg.MessageClass, updates []tg.UpdateClass) []tg.MessageClass {
	for _, update := range updates {
		switch u := update.(type) {
		case *tg.UpdateNewChannelMessage:
			messages = append(messages, u.Message)
		case *tg.UpdateEditChannelMessage:
			messages = append(messages, u.Message)
		}
	}
	return messages
}

func (r *Router) applyChannelUpdateRanks(ctx context.Context, updates []tg.UpdateClass, cache *viewerPeerCache) {
	r.applyChannelMessageRanksWithCache(ctx, channelRankMessages(nil, updates), &cache.messageRanks)
}
