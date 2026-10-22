package rpc

import (
	"context"
	"errors"
	"strconv"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) botAPISendContent(ctx context.Context, botID, chatID int64, text string, entities []domain.MessageEntity, media *domain.MessageMedia, rich *domain.MessageRichMessage, markup *domain.MessageReplyMarkup, silent, protect bool, replyID int, effect int64, options domain.BotAPIMessageOptions) (domain.Message, error) {
	return r.botAPIBroadcastMessage(ctx, botID, strconv.FormatInt(randomNonZeroInt64(), 10), options.AllowPaidBroadcast, func(sendCtx context.Context) (domain.Message, error) {
		return r.botAPISendContentOnce(sendCtx, botID, chatID, text, entities, media, rich, markup, silent, protect, replyID, effect, options)
	})
}

func (r *Router) botAPISendContentOnce(ctx context.Context, botID, chatID int64, text string, entities []domain.MessageEntity, media *domain.MessageMedia, rich *domain.MessageRichMessage, markup *domain.MessageReplyMarkup, silent, protect bool, replyID int, effect int64, options domain.BotAPIMessageOptions) (domain.Message, error) {
	protect = protect || options.ProtectContent
	if effect == 0 {
		effect = options.EffectID
	}
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return domain.Message{}, err
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	actor := botID
	if options.BusinessConnectionID != "" {
		var err error
		actor, err = r.botAPIBusinessPeer(ctx, botID, options.BusinessConnectionID, "can_reply", peer)
		if err != nil {
			return domain.Message{}, err
		}
		if peer.Type != domain.PeerTypeUser {
			return domain.Message{}, errors.New("CHAT_ID_INVALID")
		}
	}
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > domain.MaxMessageTextLength {
		return domain.Message{}, errors.New("MESSAGE_TOO_LONG")
	}
	if err := domain.ValidateReplyMarkup(markup); err != nil {
		return domain.Message{}, replyMarkupErr(err)
	}
	if err := r.validateReplyMarkupForPeer(ctx, botID, peer, markup); err != nil {
		return domain.Message{}, err
	}
	if err := domain.ValidateReplyMarkupMedia(markup, media); err != nil {
		return domain.Message{}, replyMarkupErr(err)
	}
	entities = r.augmentBotAPIAutoEntities(text, entities)
	reply, err := r.botAPIMessageReply(ctx, botID, actor, peer, replyID, options)
	if err != nil {
		return domain.Message{}, err
	}
	if options.MessageThreadID != 0 {
		if options.MessageThreadID < 0 || options.MessageThreadID > domain.MaxMessageBoxID {
			return domain.Message{}, messageIDInvalidErr()
		}
		if peer.Type == domain.PeerTypeUser {
			if err := r.botAPIPrivateTopic(ctx, botID, peer.ID, options.MessageThreadID); err != nil {
				return domain.Message{}, err
			}
		}
		if reply == nil {
			reply = &domain.MessageReply{Peer: peer}
		}
		reply.TopMessageID, reply.ForumTopic = options.MessageThreadID, true
	}
	if peer.Type != domain.PeerTypeChannel && (options.DirectMessagesTopicID != 0 || options.SuggestedPost != nil) {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	if peer.Type == domain.PeerTypeChannel {
		view, err := r.deps.Channels.ResolveChannel(ctx, botID, peer.ID)
		if err != nil {
			return domain.Message{}, err
		}
		if view.Channel.Monoforum || options.DirectMessagesTopicID != 0 || options.SuggestedPost != nil {
			return r.botAPISendMonoforumContent(ctx, botID, peer, text, entities, media, rich, markup, silent, protect, reply, options)
		}
		return r.botAPISendChannelMessage(ctx, botID, peer.ID, text, entities, media, rich, markup, silent, protect, reply, options.AlbumGroupID)
	}
	if r.deps.Messages == nil {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	res, err := r.deps.Messages.SendPrivateText(ctx, actor, domain.SendPrivateTextRequest{SenderUserID: actor, RecipientUserID: peer.ID, RandomID: randomNonZeroInt64(), Message: text, Entities: entities, Media: media, RichMessage: rich, ReplyMarkup: markup, Silent: silent, NoForwards: protect, ReplyTo: reply, Effect: effect, GroupedID: options.AlbumGroupID, Date: int(r.clock.Now().Unix()), BusinessConnectionID: options.BusinessConnectionID, BusinessBotID: businessBotForConnection(botID, options.BusinessConnectionID)})
	if err != nil {
		return domain.Message{}, err
	}
	if !res.Duplicate {
		r.finishMessageDraftAfterSend(ctx, botID, peer.ID, options.MessageThreadID)
	}
	if options.BusinessConnectionID != "" && !res.Duplicate {
		r.botAPIEnqueueBusinessMessage(ctx, botID, options.BusinessConnectionID, res.SenderMessage, false)
	}
	return res.SenderMessage, nil
}
func businessBotForConnection(botID int64, connection string) int64 {
	if connection != "" {
		return botID
	}
	return 0
}

func (r *Router) BotAPISendMessageWithOptions(ctx context.Context, botID, chatID int64, text string, entities []domain.MessageEntity, markup *domain.MessageReplyMarkup, noPreview, silent bool, replyID int, options domain.BotAPIMessageOptions) (domain.Message, error) {
	if text == "" {
		return domain.Message{}, errors.New("MESSAGE_EMPTY")
	}
	media := r.webPageMediaFromText(ctx, text, tgMessageEntities(entities), noPreview, false)
	return r.botAPISendContent(ctx, botID, chatID, text, entities, media, nil, markup, silent, options.ProtectContent, replyID, options.EffectID, options)
}
func (r *Router) BotAPISendRichMessageWithOptions(ctx context.Context, botID, chatID int64, input domain.BotAPIRichMessageInput, markup *domain.MessageReplyMarkup, silent, protect bool, replyID int, effect int64, options domain.BotAPIMessageOptions) (domain.Message, error) {
	wire, err := tgInputRichMessageFromBotAPI(input)
	if err != nil {
		return domain.Message{}, err
	}
	rich, err := r.domainRichMessageFromInput(ctx, wire)
	if err != nil {
		return domain.Message{}, err
	}
	if rich.IsZero() {
		return domain.Message{}, richMessageInvalidErr()
	}
	return r.botAPISendContent(ctx, botID, chatID, "", nil, nil, rich, markup, silent, protect, replyID, effect, options)
}
func (r *Router) BotAPISendMediaFilesWithOptions(ctx context.Context, botID, chatID int64, kind string, file, secondary domain.BotAPIFileInput, caption string, entities []domain.MessageEntity, markup *domain.MessageReplyMarkup, silent bool, replyID int, options domain.BotAPIMessageOptions) (domain.Message, error) {
	media, err := r.botAPIEphemeralMedia(ctx, botID, kind, file, secondary)
	if err != nil {
		return domain.Message{}, err
	}
	return r.botAPISendContent(ctx, botID, chatID, caption, entities, media, nil, markup, silent, false, replyID, 0, options)
}
func (r *Router) BotAPISendOrdinary(ctx context.Context, input domain.BotAPIEphemeralSendInput, options domain.BotAPIMessageOptions) (domain.Message, error) {
	media := input.DirectMedia
	if media == nil && input.Kind != "message" {
		var err error
		media, err = r.botAPIEphemeralMedia(ctx, input.BotUserID, input.Kind, input.File, input.SecondaryFile)
		if err != nil {
			return domain.Message{}, err
		}
	}
	if media != nil {
		if media.Geo != nil && media.Geo.AccessHash == 0 {
			media.Geo.AccessHash, _ = randomGeoAccessHash()
		}
		if media.Venue != nil && media.Venue.Geo.AccessHash == 0 {
			media.Venue.Geo.AccessHash, _ = randomGeoAccessHash()
		}
		if media.GeoLive != nil && media.GeoLive.Geo.AccessHash == 0 {
			media.GeoLive.Geo.AccessHash, _ = randomGeoAccessHash()
		}
	}
	return r.botAPISendContent(ctx, input.BotUserID, input.ChatID, input.Text, input.Entities, media, nil, input.ReplyMarkup, input.Silent, input.ProtectContent, input.ReplyToMessageID, input.EffectID, options)
}
func (r *Router) BotAPIEditMessageTextWithOptions(ctx context.Context, botID, chatID int64, id int, text string, entities []domain.MessageEntity, set bool, markup *domain.MessageReplyMarkup, noPreview bool, options domain.BotAPIMessageOptions) (domain.Message, error) {
	if err := r.botAPIValidateEditThread(ctx, botID, chatID, id, options); err != nil {
		return domain.Message{}, err
	}
	if options.BusinessConnectionID == "" {
		return r.BotAPIEditMessageText(ctx, botID, chatID, id, text, entities, set, markup, noPreview)
	}
	return r.botAPIBusinessEditContent(ctx, botID, chatID, id, text, entities, nil, set, markup, options)
}
func (r *Router) BotAPIEditRichMessageWithOptions(ctx context.Context, botID, chatID int64, id int, input domain.BotAPIRichMessageInput, set bool, markup *domain.MessageReplyMarkup, options domain.BotAPIMessageOptions) (domain.Message, error) {
	if err := r.botAPIValidateEditThread(ctx, botID, chatID, id, options); err != nil {
		return domain.Message{}, err
	}
	if options.BusinessConnectionID == "" {
		return r.BotAPIEditRichMessage(ctx, botID, chatID, id, input, set, markup)
	}
	wire, err := tgInputRichMessageFromBotAPI(input)
	if err != nil {
		return domain.Message{}, err
	}
	rich, err := r.domainRichMessageFromInput(ctx, wire)
	if err != nil {
		return domain.Message{}, err
	}
	return r.botAPIBusinessEditContent(ctx, botID, chatID, id, "", nil, rich, set, markup, options)
}
func (r *Router) botAPIBusinessEditContent(ctx context.Context, botID, chatID int64, id int, text string, entities []domain.MessageEntity, rich *domain.MessageRichMessage, set bool, markup *domain.MessageReplyMarkup, options domain.BotAPIMessageOptions) (domain.Message, error) {
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
	if rich == nil && text == "" {
		return domain.Message{}, errors.New("MESSAGE_EMPTY")
	}
	if utf8.RuneCountInString(text) > domain.MaxMessageTextLength {
		return domain.Message{}, errors.New("MESSAGE_TOO_LONG")
	}
	if set {
		if err := domain.ValidateReplyMarkup(markup); err != nil {
			return domain.Message{}, replyMarkupErr(err)
		}
		if err := r.validateReplyMarkupForPeer(ctx, botID, peer, markup); err != nil {
			return domain.Message{}, err
		}
		if err := domain.ValidateReplyMarkupMedia(markup, current.Media); err != nil {
			return domain.Message{}, replyMarkupErr(err)
		}
	}
	res, err := r.deps.Messages.EditMessage(ctx, owner, domain.EditMessageRequest{OwnerUserID: owner, Peer: peer, ID: id, Message: text, Entities: r.augmentBotAPIAutoEntities(text, entities), SetRichMessage: true, RichMessage: rich, SetReplyMarkup: set, ReplyMarkup: markup, EditDate: int(r.clock.Now().Unix())})
	if err != nil {
		return domain.Message{}, messageEditErr(err)
	}
	r.enqueueBotAPIPrivateEditUpdatesAsync(ctx, res)
	msg := res.Self().Message
	r.botAPIEnqueueBusinessMessage(ctx, botID, options.BusinessConnectionID, msg, true)
	return msg, nil
}

func (r *Router) botAPIValidateEditThread(ctx context.Context, botID, chatID int64, id int, options domain.BotAPIMessageOptions) error {
	if options.MessageThreadID == 0 {
		return nil
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return errors.New("CHAT_ID_INVALID")
	}
	owner := botID
	if options.BusinessConnectionID != "" {
		var err error
		owner, err = r.botAPIBusinessPeer(ctx, botID, options.BusinessConnectionID, "can_reply", peer)
		if err != nil {
			return err
		}
	}
	msg, err := r.botAPIReloadMessage(ctx, owner, peer, id)
	if err != nil {
		return err
	}
	if msg.ReplyTo == nil || msg.ReplyTo.TopMessageID != options.MessageThreadID {
		return errors.New("MESSAGE_THREAD_INVALID")
	}
	return nil
}
