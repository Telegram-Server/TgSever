package rpc

import (
	"context"
	"errors"
	"telesrv/internal/domain"
)

func (r *Router) botAPIBusinessStoredEdit(ctx context.Context, botID, chatID int64, id int, options domain.BotAPIMessageOptions, edit botAPIStoredEdit, caption bool) (domain.Message, error) {
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok || peer.Type != domain.PeerTypeUser {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	owner, err := r.botAPIBusinessPeer(ctx, botID, options.BusinessConnectionID, "can_reply", peer)
	if err != nil {
		return domain.Message{}, err
	}
	current, err := r.botAPIBusinessEditTarget(ctx, botID, owner, options.BusinessConnectionID, peer, id)
	if err != nil {
		return domain.Message{}, err
	}
	if err := r.botAPIValidateEditThread(ctx, botID, chatID, id, options); err != nil {
		return domain.Message{}, err
	}
	if edit.media != nil {
		if err := botAPIValidateMediaReplacement(current, edit.media); err != nil {
			return domain.Message{}, err
		}
	}
	if caption {
		if current.Media == nil || current.Media.Kind != domain.MessageMediaKindPhoto && current.Media.Kind != domain.MessageMediaKindDocument {
			return domain.Message{}, errors.New("MEDIA_PREV_INVALID")
		}
	}
	if caption || edit.media != nil {
		current.Body, current.Entities = edit.caption, r.augmentBotAPIAutoEntities(edit.caption, edit.entities)
	}
	media := edit.media
	if edit.setReplyMarkup {
		effective := current.Media
		if media != nil {
			effective = media
		}
		if err := domain.ValidateReplyMarkupMedia(edit.replyMarkup, effective); err != nil {
			return domain.Message{}, replyMarkupErr(err)
		}
	}
	return r.botAPIBusinessEditMedia(ctx, botID, owner, options.BusinessConnectionID, current, media, false, edit.setReplyMarkup, edit.replyMarkup)
}
func (r *Router) BotAPIEditMessageCaptionWithOptions(ctx context.Context, botID, chatID int64, id int, caption string, entities []domain.MessageEntity, set bool, markup *domain.MessageReplyMarkup, options domain.BotAPIMessageOptions) (domain.Message, error) {
	if options.BusinessConnectionID == "" {
		return r.BotAPIEditMessageCaption(ctx, botID, chatID, id, caption, entities, set, markup)
	}
	return r.botAPIBusinessStoredEdit(ctx, botID, chatID, id, options, botAPIStoredEdit{caption: caption, entities: entities, setReplyMarkup: set, replyMarkup: markup}, true)
}
func (r *Router) BotAPIEditMessageReplyMarkupWithOptions(ctx context.Context, botID, chatID int64, id int, markup *domain.MessageReplyMarkup, options domain.BotAPIMessageOptions) (domain.Message, error) {
	if options.BusinessConnectionID == "" {
		return r.BotAPIEditMessageReplyMarkup(ctx, botID, chatID, id, markup)
	}
	return r.botAPIBusinessStoredEdit(ctx, botID, chatID, id, options, botAPIStoredEdit{setReplyMarkup: true, replyMarkup: markup}, false)
}
func (r *Router) BotAPIEditMessageMediaWithOptions(ctx context.Context, botID, chatID int64, id int, item BotAPIInputMediaItem, set bool, markup *domain.MessageReplyMarkup, options domain.BotAPIMessageOptions) (domain.Message, error) {
	if options.BusinessConnectionID == "" {
		return r.BotAPIEditMessageMedia(ctx, botID, chatID, id, item, set, markup)
	}
	media, err := r.botAPIEphemeralMedia(ctx, botID, item.Kind, item.File, item.SecondaryFile)
	if err != nil {
		return domain.Message{}, err
	}
	media.Spoiler = item.Spoiler
	return r.botAPIBusinessStoredEdit(ctx, botID, chatID, id, options, botAPIStoredEdit{media: media, caption: item.Caption, entities: item.Entities, setReplyMarkup: set, replyMarkup: markup}, false)
}
func (r *Router) BotAPIStopPollWithOptions(ctx context.Context, botID, chatID int64, id int, set bool, markup *domain.MessageReplyMarkup, options domain.BotAPIMessageOptions) (domain.MessagePoll, error) {
	if options.BusinessConnectionID == "" {
		return r.BotAPIStopPoll(ctx, botID, chatID, id, set, markup)
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok || peer.Type != domain.PeerTypeUser {
		return domain.MessagePoll{}, errors.New("CHAT_ID_INVALID")
	}
	owner, err := r.botAPIBusinessPeer(ctx, botID, options.BusinessConnectionID, "can_reply", peer)
	if err != nil {
		return domain.MessagePoll{}, err
	}
	current, err := r.botAPIBusinessEditTarget(ctx, botID, owner, options.BusinessConnectionID, peer, id)
	if err != nil {
		return domain.MessagePoll{}, err
	}
	if current.Media == nil || current.Media.Poll == nil {
		return domain.MessagePoll{}, errors.New("MESSAGE_POLL_INVALID")
	}
	if current.Media.Poll.Closed {
		return domain.MessagePoll{}, errors.New("MESSAGE_POLL_CLOSED")
	}
	if err := r.botAPIClosePoll(ctx, owner, chatID, id); err != nil {
		return domain.MessagePoll{}, err
	}
	msg, err := r.botAPIReloadMessage(ctx, owner, peer, id)
	if err != nil {
		return domain.MessagePoll{}, err
	}
	if set {
		msg, err = r.botAPIBusinessEditMedia(ctx, botID, owner, options.BusinessConnectionID, msg, nil, false, true, markup)
		if err != nil {
			return domain.MessagePoll{}, err
		}
	} else {
		r.botAPIEnqueueBusinessMessage(ctx, botID, options.BusinessConnectionID, msg, true)
	}
	return *msg.Media.Poll, nil
}
