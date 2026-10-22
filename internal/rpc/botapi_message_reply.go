package rpc

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

// botAPIMessageReply resolves Bot API identifiers as the authenticated sending
// principal. It returns the full intent, not a pre-resolved fallback, so stores
// can atomically resolve missing replies without changing idempotency inputs.
func (r *Router) botAPIMessageReply(ctx context.Context, botID, actorID int64, to domain.Peer, legacyID int, options domain.BotAPIMessageOptions) (*domain.MessageReply, error) {
	parameters := options.ReplyParameters
	if parameters == nil {
		if legacyID == 0 {
			return nil, nil
		}
		parameters = &domain.BotAPIReplyParameters{MessageID: legacyID}
	}
	if parameters.EphemeralMessageID != 0 || parameters.MessageID <= 0 || parameters.MessageID > domain.MaxMessageBoxID {
		return nil, replyMessageIDInvalidErr()
	}
	sourcePeer := to
	if parameters.ChatID != "" {
		if options.BusinessConnectionID != "" {
			return nil, replyMessageIDInvalidErr()
		}
		chatID, err := r.botAPIReplyChatID(ctx, actorID, parameters.ChatID)
		if err != nil {
			return nil, err
		}
		_, sourcePeer, err = r.botAPIInputPeer(ctx, actorID, chatID)
		if err != nil {
			return nil, err
		}
	}
	allowMissing := parameters.AllowSendingWithoutReply && sourcePeer == to && options.MessageThreadID == 0
	if options.BusinessConnectionID != "" {
		allowMissing = true
	}
	if to.Type == domain.PeerTypeChannel {
		view, err := r.deps.Channels.ResolveChannel(ctx, actorID, to.ID)
		if err != nil {
			return nil, err
		}
		if view.Channel.Monoforum && (sourcePeer != to || parameters.ChatID != "") {
			return nil, replyMessageIDInvalidErr()
		}
		if view.Channel.Forum {
			allowMissing = false
		}
	}
	reply := &domain.MessageReply{MessageID: parameters.MessageID, Peer: sourcePeer, QuoteText: parameters.Quote, QuoteEntities: append([]domain.MessageEntity(nil), parameters.QuoteEntities...), ChecklistTaskID: parameters.ChecklistTaskID, AllowMissing: allowMissing}
	if parameters.QuotePosition != nil {
		reply.QuoteOffset = *parameters.QuotePosition
	}
	if parameters.PollOptionID != "" {
		var err error
		reply.PollOption, err = base64.RawURLEncoding.DecodeString(parameters.PollOptionID)
		if err != nil || len(reply.PollOption) == 0 {
			return nil, pollOptionInvalidErr()
		}
	}
	if err := domain.ValidateMessageReplyBounds(reply); err != nil {
		return nil, replyMessageIDInvalidErr()
	}
	source, found, err := r.botAPIReplySource(ctx, actorID, sourcePeer, parameters.MessageID)
	if err != nil {
		return nil, err
	}
	if !found {
		if allowMissing {
			return reply, nil
		}
		return nil, replyMessageIDInvalidErr()
	}
	if source.ReplyTo != nil && source.ReplyTo.ForumTopic {
		reply.AllowMissing = false
	}
	if sourcePeer != to {
		if source.NoForwards || source.Media != nil && (source.Media.TTLSeconds > 0 || source.Media.PaidMedia != nil) {
			return nil, domain.ErrChatForwardsRestricted
		}
		if sourcePeer.Type == domain.PeerTypeUser {
			if privacy, ok := r.deps.Messages.(PrivateNoForwardsService); ok {
				state, err := privacy.GetPrivateNoForwards(ctx, actorID, sourcePeer.ID)
				if err != nil {
					return nil, err
				}
				if state.Enabled() {
					return nil, domain.ErrChatForwardsRestricted
				}
			}
		}
	}
	if parameters.QuotePosition == nil && reply.QuoteText != "" {
		text, _, err := domain.BotAPIReplySourceText(reply, source)
		if err != nil {
			return nil, err
		}
		matched := false
		byteFrom, consumed, offset := 0, 0, 0
		for byteFrom <= len(text)-len(reply.QuoteText) {
			relative := strings.Index(text[byteFrom:], reply.QuoteText)
			if relative < 0 {
				break
			}
			start := byteFrom + relative
			for _, character := range text[consumed:start] {
				offset++
				if character > 0xffff {
					offset++
				}
			}
			consumed = start
			reply.QuoteOffset = offset
			if domain.ValidateBotAPIReplySource(reply, source) == nil {
				matched = true
				break
			}
			byteFrom = start + 1
		}
		if !matched {
			return nil, domain.ErrQuoteTextInvalid
		}
	}
	if err := domain.ValidateBotAPIReplySource(reply, source); err != nil {
		return nil, err
	}
	return reply, nil
}

func (r *Router) botAPIReplyChatID(ctx context.Context, actorID int64, raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "@") {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id == 0 {
			return 0, replyMessageIDInvalidErr()
		}
		return id, nil
	}
	if users, ok := r.deps.Users.(UserIdentityService); ok {
		user, found, err := users.ResolveUsername(ctx, actorID, strings.TrimPrefix(raw, "@"))
		if err != nil {
			return 0, err
		}
		if found && user.Bot {
			return user.ID, nil
		}
	}
	return r.BotAPIResolveChatUsername(ctx, actorID, raw)
}

func (r *Router) botAPIReplySource(ctx context.Context, actorID int64, peer domain.Peer, id int) (domain.Message, bool, error) {
	if peer.Type == domain.PeerTypeUser {
		if r.deps.Messages == nil {
			return domain.Message{}, false, errors.New("BOT_INVALID")
		}
		list, err := r.deps.Messages.GetMessages(ctx, actorID, []int{id})
		if err != nil {
			return domain.Message{}, false, err
		}
		for _, message := range list.Messages {
			if message.ID == id && message.Peer == peer && !message.Deleted {
				return message, true, nil
			}
		}
		return domain.Message{}, false, nil
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		history, err := r.deps.Channels.GetMessages(ctx, actorID, peer.ID, []int{id})
		if err != nil {
			return domain.Message{}, false, err
		}
		for _, message := range history.Messages {
			if message.ID == id && !message.Deleted {
				return botAPIMessageFromChannel(actorID, message), true, nil
			}
		}
		return domain.Message{}, false, nil
	}
	return domain.Message{}, false, replyMessageIDInvalidErr()
}
