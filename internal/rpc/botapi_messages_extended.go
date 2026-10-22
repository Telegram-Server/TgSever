package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

type BotAPILiveLocationEdit = struct {
	Stop                                          bool
	Latitude, Longitude, Accuracy                 float64
	Period, Heading, Radius                       int
	SetAccuracy, SetPeriod, SetHeading, SetRadius bool
}
type BotAPIMessageDraft = struct {
	ID                  int64
	ThreadID            int
	Text                string
	Entities            []domain.MessageEntity
	Rich                *domain.BotAPIRichMessageInput
	CanStop, KeepOnStop bool
}
type BotAPIChecklistSendOptions = struct {
	Silent, Protect bool
	EffectID        int64
	ReplyID         int
	ReplyParameters *domain.BotAPIReplyParameters
}

func (r *Router) BotAPIDeleteMessages(ctx context.Context, botID, chatID int64, ids []int) (bool, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return false, err
	}
	if len(ids) < 1 || len(ids) > 100 {
		return false, errors.New("MESSAGE_IDS_INVALID")
	}
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return false, messageIDInvalidErr()
		}
	}
	_, peer, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return false, err
	}
	if peer.Type == domain.PeerTypeChannel {
		history, err := r.deps.Channels.GetMessages(ctx, botID, peer.ID, ids)
		if err != nil {
			return false, err
		}
		if history.Self.Status != domain.ChannelMemberActive {
			return false, errors.New("CHAT_ID_INVALID")
		}
		selected := make([]int, 0, len(history.Messages))
		for _, msg := range history.Messages {
			if botAPIDeletionAllowed(botAPIMessageFromChannel(botID, msg), int(r.clock.Now().Unix())) {
				selected = append(selected, msg.ID)
			}
		}
		if len(selected) == 0 {
			return true, nil
		}
		ids = selected
		res, err := r.deps.Channels.DeleteMessages(ctx, botID, domain.DeleteChannelMessagesRequest{UserID: botID, ChannelID: peer.ID, IDs: ids, SkipUnauthorized: true, Date: int(r.clock.Now().Unix()), Audit: domain.BotDeletionAudit(botID)})
		if err != nil {
			return false, channelDeleteErr(err)
		}
		r.NotifyModerationChannelDeletion(ctx, res)
		return true, nil
	}
	list, err := r.deps.Messages.GetMessages(ctx, botID, ids)
	if err != nil {
		return false, err
	}
	selected := make([]int, 0, len(list.Messages))
	for _, msg := range list.Messages {
		if msg.Peer == peer && botAPIDeletionAllowed(msg, int(r.clock.Now().Unix())) {
			selected = append(selected, msg.ID)
		}
	}
	if len(selected) == 0 {
		return true, nil
	}
	res, err := r.deps.Messages.DeleteMessages(ctx, botID, domain.DeleteMessagesRequest{OwnerUserID: botID, IDs: selected, Revoke: true, Date: int(r.clock.Now().Unix()), Audit: domain.BotDeletionAudit(botID)})
	if err != nil {
		return false, err
	}
	r.notifyPushDeletes(res)
	r.enqueueBotAPIBusinessPrivateDeletes(ctx, res)
	return true, nil
}

func (r *Router) BotAPISetMessageReaction(ctx context.Context, botID, chatID int64, id int, reactions []domain.MessageReaction, big bool) (bool, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return false, err
	}
	if len(reactions) > 1 {
		return false, errors.New("REACTIONS_TOO_MANY")
	}
	input, _, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return false, err
	}
	wire := make([]tg.ReactionClass, 0, len(reactions))
	for _, reaction := range reactions {
		if reaction.Type == domain.MessageReactionEmoji {
			if err := r.validateDefaultReaction(ctx, reaction); err != nil {
				return false, err
			}
		}
	}
	for _, reaction := range reactions {
		switch reaction.Type {
		case domain.MessageReactionEmoji:
			wire = append(wire, &tg.ReactionEmoji{Emoticon: reaction.Emoticon})
		case domain.MessageReactionCustomEmoji:
			if reaction.DocumentID <= 0 {
				return false, reactionInvalidErr()
			}
			wire = append(wire, &tg.ReactionCustomEmoji{DocumentID: reaction.DocumentID})
		default:
			return false, reactionInvalidErr()
		}
	}
	req := &tg.MessagesSendReactionRequest{Peer: input, MsgID: id, Big: big}
	req.SetReaction(wire)
	_, err = r.onMessagesSendReaction(WithUserID(ctx, botID), req)
	return err == nil, err
}

func (r *Router) BotAPIDeleteMessageReaction(ctx context.Context, botID, chatID int64, id int, actor domain.Peer) (bool, error) {
	peer, err := r.botAPIReactionModerationPeer(ctx, botID, chatID, actor)
	if err != nil {
		return false, err
	}
	_, err = r.deleteParticipantReaction(ctx, botID, peer, id, actor)
	return err == nil, err
}
func (r *Router) BotAPIDeleteAllMessageReactions(ctx context.Context, botID, chatID int64, actor domain.Peer) (bool, error) {
	peer, err := r.botAPIReactionModerationPeer(ctx, botID, chatID, actor)
	if err != nil {
		return false, err
	}
	return r.deleteParticipantReactions(ctx, botID, peer, actor)
}
func (r *Router) botAPIReactionModerationPeer(ctx context.Context, botID, chatID int64, actor domain.Peer) (domain.Peer, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return domain.Peer{}, err
	}
	_, peer, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return domain.Peer{}, err
	}
	if peer.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return domain.Peer{}, errors.New("CHAT_ID_INVALID")
	}
	history, err := r.deps.Channels.GetMessages(ctx, botID, peer.ID, nil)
	if err != nil {
		return domain.Peer{}, err
	}
	if history.Channel.Broadcast {
		return domain.Peer{}, errors.New("CHAT_ID_INVALID")
	}
	if !domain.ReactionActorValid(actor) {
		return domain.Peer{}, errors.New("PARTICIPANT_ID_INVALID")
	}
	return peer, nil
}

func botAPILiveMedia(input BotAPILiveLocationEdit) *tg.InputMediaGeoLive {
	media := &tg.InputMediaGeoLive{Stopped: input.Stop, GeoPoint: &tg.InputGeoPointEmpty{}}
	if !input.Stop {
		geo := &tg.InputGeoPoint{Lat: input.Latitude, Long: input.Longitude}
		if input.SetAccuracy {
			geo.SetAccuracyRadius(int(input.Accuracy + 0.5))
		}
		media.GeoPoint = geo
		if input.SetPeriod {
			media.SetPeriod(input.Period)
		}
		if input.SetHeading {
			media.SetHeading(input.Heading)
		}
		if input.SetRadius {
			media.SetProximityNotificationRadius(input.Radius)
		}
	}
	return media
}
func (r *Router) BotAPIEditLiveLocation(ctx context.Context, botID, chatID int64, id int, connection string, input BotAPILiveLocationEdit, set bool, markup *domain.MessageReplyMarkup) (domain.Message, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return domain.Message{}, err
	}
	if connection != "" {
		peer, ok := botAPIPeerFromChatID(chatID)
		if !ok || peer.Type != domain.PeerTypeUser {
			return domain.Message{}, errors.New("CHAT_ID_INVALID")
		}
		owner, err := r.botAPIBusinessPeer(ctx, botID, connection, "can_reply", peer)
		if err != nil {
			return domain.Message{}, err
		}
		current, err := r.botAPIBusinessEditTarget(ctx, botID, owner, connection, peer, id)
		if err != nil {
			return domain.Message{}, err
		}
		media, err := editLiveLocationMedia(current.Media, current.Date, int(r.clock.Now().Unix()), botAPILiveMedia(input))
		if err != nil {
			return domain.Message{}, err
		}
		return r.botAPIBusinessEditMedia(ctx, botID, owner, connection, current, media, false, set, markup)
	}
	req := &tg.MessagesEditMessageRequest{ID: id}
	req.SetMedia(botAPILiveMedia(input))
	if wire := botAPIEditReplyMarkup(set, markup); wire != nil {
		req.SetReplyMarkup(wire)
	}
	return r.botAPIEditViaHandler(ctx, botID, chatID, req)
}
func (r *Router) BotAPIEditInlineLiveLocation(ctx context.Context, botID int64, id domain.BotInlineMessageID, input BotAPILiveLocationEdit, set bool, markup *domain.MessageReplyMarkup) (bool, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return false, err
	}
	req := &tg.MessagesEditInlineBotMessageRequest{ID: tgInputBotInlineMessageID(id)}
	req.SetMedia(botAPILiveMedia(input))
	if wire := botAPIEditReplyMarkup(set, markup); wire != nil {
		req.SetReplyMarkup(wire)
	}
	return r.onMessagesEditInlineBotMessage(WithUserID(ctx, botID), req)
}
func botAPITodoMedia(todo domain.MessageTodo) *tg.InputMediaTodo {
	return &tg.InputMediaTodo{Todo: tg.TodoList{Title: tgTextWithEntities(todo.Title, todo.TitleEntities), List: tgTodoItems(todo.Items), OthersCanAppend: todo.OthersCanAppend, OthersCanComplete: todo.OthersCanComplete}}
}
func (r *Router) BotAPISendChecklist(ctx context.Context, botID, chatID int64, connection string, todo domain.MessageTodo, options BotAPIChecklistSendOptions, markup *domain.MessageReplyMarkup) (domain.Message, error) {
	if connection == "" {
		return domain.Message{}, errors.New("BUSINESS_CONNECTION_INVALID")
	}
	validated, err := domainTodoFromInput(botAPITodoMedia(todo).Todo)
	if err != nil {
		return domain.Message{}, err
	}
	return r.botAPISendContent(ctx, botID, chatID, "", nil, &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: validated}, nil, markup, options.Silent, options.Protect, options.ReplyID, options.EffectID, domain.BotAPIMessageOptions{BusinessConnectionID: connection, ReplyParameters: options.ReplyParameters})
}
func (r *Router) BotAPIEditChecklist(ctx context.Context, botID, chatID int64, id int, connection string, todo domain.MessageTodo, set bool, markup *domain.MessageReplyMarkup) (domain.Message, error) {
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok || peer.Type != domain.PeerTypeUser {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	owner, err := r.botAPIBusinessPeer(ctx, botID, connection, "can_reply", peer)
	if err != nil {
		return domain.Message{}, err
	}
	current, err := r.botAPIBusinessEditTarget(ctx, botID, owner, connection, peer, id)
	if err != nil {
		return domain.Message{}, err
	}
	if current.Media == nil || current.Media.Todo == nil {
		return domain.Message{}, messageIDInvalidErr()
	}
	validated, err := domainTodoFromInput(botAPITodoMedia(todo).Todo)
	if err != nil {
		return domain.Message{}, err
	}
	return r.botAPIBusinessEditMedia(ctx, botID, owner, connection, current, &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: validated}, true, set, markup)
}

func (r *Router) BotAPISendDraft(ctx context.Context, botID, chatID int64, draft BotAPIMessageDraft) (bool, error) {
	if chatID <= 0 {
		return false, errors.New("CHAT_ID_INVALID")
	}
	if draft.ID == 0 {
		return false, errors.New("DRAFT_ID_INVALID")
	}
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return false, err
	}
	if draft.ThreadID != 0 {
		if err := r.botAPIPrivateTopic(ctx, botID, chatID, draft.ThreadID); err != nil {
			return false, err
		}
	}
	input, _, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return false, err
	}
	var action tg.SendMessageActionClass = &tg.SendMessageTextDraftAction{RandomID: draft.ID, Text: tgTextWithEntities(draft.Text, draft.Entities), CanStop: draft.CanStop, KeepOnStop: draft.KeepOnStop}
	if draft.Rich != nil {
		rich, err := tgInputRichMessageFromBotAPI(*draft.Rich)
		if err != nil {
			return false, err
		}
		action = &tg.InputSendMessageRichMessageDraftAction{RandomID: draft.ID, RichMessage: rich, CanStop: draft.CanStop, KeepOnStop: draft.KeepOnStop}
	}
	req := &tg.MessagesSetTypingRequest{Peer: input, Action: action}
	if draft.ThreadID != 0 {
		req.SetTopMsgID(draft.ThreadID)
	}
	return r.onMessagesSetTyping(WithUserID(ctx, botID), req)
}

func (r *Router) botAPIBusinessEditTarget(ctx context.Context, botID, owner int64, connection string, peer domain.Peer, id int) (domain.Message, error) {
	msg, err := r.botAPIReloadMessage(ctx, owner, peer, id)
	if err != nil {
		return domain.Message{}, err
	}
	if !msg.Out {
		return domain.Message{}, messageAuthorRequiredErr()
	}
	inlineKeyboard := msg.ReplyMarkup != nil && len(msg.ReplyMarkup.Inline) > 0
	if msg.BusinessBotID != botID && !inlineKeyboard && int64(r.clock.Now().Unix())-int64(msg.Date) >= 48*3600 {
		return domain.Message{}, messageEditForbiddenErr()
	}
	return msg, nil
}

func (r *Router) botAPIBusinessEditMedia(ctx context.Context, botID, owner int64, connection string, current domain.Message, media *domain.MessageMedia, replaceTodo, set bool, markup *domain.MessageReplyMarkup) (domain.Message, error) {
	if set {
		if err := domain.ValidateReplyMarkup(markup); err != nil {
			return domain.Message{}, replyMarkupErr(err)
		}
		if err := r.validateReplyMarkupForPeer(ctx, botID, current.Peer, markup); err != nil {
			return domain.Message{}, err
		}
		effective := media
		if effective == nil {
			effective = current.Media
		}
		if err := domain.ValidateReplyMarkupMedia(markup, effective); err != nil {
			return domain.Message{}, replyMarkupErr(err)
		}
	}
	res, err := r.deps.Messages.EditMessage(ctx, owner, domain.EditMessageRequest{OwnerUserID: owner, Peer: current.Peer, ID: current.ID, Message: current.Body, Entities: current.Entities, Media: media, ReplaceTodo: replaceTodo, SetRichMessage: media != nil && current.RichMessage != nil, SetReplyMarkup: set, ReplyMarkup: markup, EditDate: int(r.clock.Now().Unix())})
	if err != nil {
		return domain.Message{}, messageEditErr(err)
	}
	r.enqueueBotAPIPrivateEditUpdatesAsync(ctx, res)
	msg := res.Self().Message
	r.botAPIEnqueueBusinessMessage(ctx, botID, connection, msg, true)
	return msg, nil
}

func botAPIDeletionAllowed(msg domain.Message, now int) bool {
	if msg.Deleted || msg.Date <= 0 || int64(now)-int64(msg.Date) >= 48*3600 {
		return false
	}
	if msg.Action != nil && (msg.Action.Type == domain.ChannelActionCreate || msg.Action.Type == domain.ChannelActionTopicCreate) {
		return false
	}
	if msg.Media != nil && msg.Media.ServiceAction != nil && msg.Media.ServiceAction.Kind == domain.MessageServiceActionTopicCreate {
		return false
	}
	if msg.Peer.Type == domain.PeerTypeUser && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindDice && int64(now)-int64(msg.Date) <= 24*3600 {
		return false
	}
	return true
}
