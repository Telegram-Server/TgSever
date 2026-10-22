package rpc

import (
	"context"
	"errors"

	"telesrv/internal/domain"
)

// botAPISendMonoforumContent keeps direct-message topic routing and suggested
// post creation on the same authorized aggregate and fanout path as MTProto.
func (r *Router) botAPISendMonoforumContent(ctx context.Context, botID int64, peer domain.Peer, text string, entities []domain.MessageEntity, media *domain.MessageMedia, rich *domain.MessageRichMessage, markup *domain.MessageReplyMarkup, silent, protect bool, reply *domain.MessageReply, options domain.BotAPIMessageOptions) (domain.Message, error) {
	if _, err := r.callerBotID(WithUserID(ctx, botID)); err != nil {
		return domain.Message{}, err
	}
	if peer.Type != domain.PeerTypeChannel || r.deps.Channels == nil || options.BusinessConnectionID != "" {
		return domain.Message{}, peerIDInvalidErr()
	}
	if options.DirectMessagesTopicID <= 0 {
		return domain.Message{}, errors.New("DIRECT_MESSAGES_TOPIC_ID_INVALID")
	}
	if options.MessageThreadID != 0 || options.EffectID != 0 {
		return domain.Message{}, errors.New("MESSAGE_THREAD_INVALID")
	}
	mono, manager, err := r.deps.Channels.ResolveMonoforumSend(ctx, botID, peer.ID)
	if err != nil {
		return domain.Message{}, messageSendErr(err)
	}
	if !manager {
		return domain.Message{}, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	if r.deps.Users == nil {
		return domain.Message{}, userIDInvalidErr()
	}
	target, found, err := r.deps.Users.ByID(ctx, botID, int64(options.DirectMessagesTopicID))
	if err != nil {
		return domain.Message{}, err
	}
	if !found || target.Deleted {
		return domain.Message{}, userIDInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	if post := options.SuggestedPost; post != nil {
		if post.Accepted || post.Rejected || post.ScheduleDate < 0 || post.ScheduleDate > now+domain.MaxSuggestedPostScheduleDelay {
			return domain.Message{}, tgerr400("SUGGESTED_POST_INVALID")
		}
		if price := post.Price; price != nil {
			valid := price.Nanos == 0 && ((price.Kind == domain.SuggestedPostPriceStars && price.Amount >= minSuggestedPostStars && price.Amount <= maxSuggestedPostStars) || (price.Kind == domain.SuggestedPostPriceTON && price.Amount >= minSuggestedPostNanoTON && price.Amount <= maxSuggestedPostNanoTON))
			if !valid {
				return domain.Message{}, tgerr400("SUGGESTED_POST_AMOUNT_INVALID")
			}
		}
	}
	updates, err := r.sendMonoforumMessage(ctx, botID, peer, mono, manager, domain.SendMonoforumMessageRequest{
		SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: target.ID}, RandomID: randomNonZeroInt64(), Message: text, Entities: entities,
		Media: media, RichMessage: rich, ReplyMarkup: markup, ReplyTo: reply, Silent: silent, NoForwards: protect,
		SuggestedPost: options.SuggestedPost, Date: now,
	})
	if err != nil {
		return domain.Message{}, err
	}
	messages, err := r.botAPIMessagesFromUpdates(ctx, botID, peer, updates)
	if err != nil {
		return domain.Message{}, err
	}
	if len(messages) != 1 {
		return domain.Message{}, messageIDInvalidErr()
	}
	return messages[0], nil
}
