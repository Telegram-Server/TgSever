package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"strconv"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

// Called only after Vote/Close committed. The authoritative creator, not the
// forwarded message sender, decides which bot is entitled to poll updates.
func (r *Router) enqueueBotAPIPollMutation(ctx context.Context, actorID int64, poll *domain.MessagePoll, options *[][]byte) {
	if r == nil || r.deps.BotAPIUpdates == nil || r.deps.Polls == nil || poll == nil {
		return
	}
	definition, found, err := r.deps.Polls.GetPollDefinition(ctx, poll.ID)
	if err != nil || !found {
		return
	}
	botID := definition.CreatorUserID
	bot, err := r.botAPIKnownBot(ctx, botID)
	if err != nil || !bot {
		return
	}
	r.enqueueBotAPIObject(ctx, botID, domain.BotAPIUpdateKind("poll"), botapi.SnapshotPoll(*poll, nil))
	if options == nil || !definition.PublicVoters || r.deps.Users == nil {
		return
	}
	user, found, err := r.deps.Users.ByID(ctx, botID, actorID)
	if err != nil || !found {
		return
	}
	ids := make([]int, 0, len(*options))
	persistentIDs := make([]string, 0, len(*options))
	for _, option := range *options {
		for index, answer := range poll.Answers {
			if bytes.Equal(option, answer.Option) {
				ids = append(ids, index)
				persistent := base64.RawURLEncoding.EncodeToString(answer.Option)
				if persistent == "" {
					persistent = strconv.Itoa(index)
				}
				persistentIDs = append(persistentIDs, persistent)
				break
			}
		}
	}
	r.enqueueBotAPIObject(ctx, botID, domain.BotAPIUpdateKind("poll_answer"), map[string]any{"poll_id": strconv.FormatInt(poll.ID, 10), "user": botapi.SnapshotUser(user), "option_ids": ids, "option_persistent_ids": persistentIDs})
}

func (r *Router) enqueueBotAPIPrivatePollMutation(ctx context.Context, actorID int64, res domain.PrivateMessagePollResult, options *[][]byte) {
	if r == nil || r.deps.Polls == nil {
		return
	}
	definition, found, err := r.deps.Polls.GetPollDefinition(ctx, res.PollID)
	if err != nil || !found {
		return
	}
	for _, message := range res.Messages {
		if message.OwnerUserID == definition.CreatorUserID && message.Media != nil && message.Media.Poll != nil {
			r.enqueueBotAPIPollMutation(ctx, actorID, message.Media.Poll, options)
			return
		}
	}
}

func (r *Router) enqueueBotAPIChannelPollMutation(ctx context.Context, actorID int64, res domain.ChannelMessagePollResult, options *[][]byte) {
	if r == nil || r.deps.Polls == nil {
		return
	}
	definition, found, err := r.deps.Polls.GetPollDefinition(ctx, res.PollID)
	if err != nil || !found {
		return
	}
	poll, _, found := r.loadMessagePoll(ctx, definition.CreatorUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: res.Channel.ID}, res.Message.ID)
	if found {
		r.enqueueBotAPIPollMutation(ctx, actorID, poll, options)
	}
}
