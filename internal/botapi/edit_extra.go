package botapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"telesrv/internal/domain"
)

// EditExtraGateway 是 editMessageCaption / editMessageReplyMarkup / editMessageMedia /
// stopPoll 的网关。chat_id+message_id 形式返回编辑后的消息，inline_message_id 形式
// 返回 true。可选接口：网关不实现时这些方法回 METHOD_NOT_FOUND。
type EditExtraGateway interface {
	BotAPIEditMessageCaption(ctx context.Context, botID, chatID int64, messageID int, caption string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error)
	BotAPIEditInlineMessageCaption(ctx context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, caption string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (bool, error)
	// replyMarkup 为 nil 即清空键盘。
	BotAPIEditMessageReplyMarkup(ctx context.Context, botID, chatID int64, messageID int, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error)
	BotAPIEditInlineMessageReplyMarkup(ctx context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, replyMarkup *domain.MessageReplyMarkup) (bool, error)
	BotAPIEditMessageMedia(ctx context.Context, botID, chatID int64, messageID int, item InputMediaItem, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error)
	BotAPIEditInlineMessageMedia(ctx context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, item InputMediaItem, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (bool, error)
	// BotAPIStopPoll 关闭机器人发出的投票并返回关闭后的投票（含最终结果）。
	BotAPIStopPoll(ctx context.Context, botID, chatID int64, messageID int, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.MessagePoll, error)
}

type BusinessEditExtraGateway interface {
	BotAPIEditMessageCaptionWithOptions(context.Context, int64, int64, int, string, []domain.MessageEntity, bool, *domain.MessageReplyMarkup, domain.BotAPIMessageOptions) (domain.Message, error)
	BotAPIEditMessageReplyMarkupWithOptions(context.Context, int64, int64, int, *domain.MessageReplyMarkup, domain.BotAPIMessageOptions) (domain.Message, error)
	BotAPIEditMessageMediaWithOptions(context.Context, int64, int64, int, InputMediaItem, bool, *domain.MessageReplyMarkup, domain.BotAPIMessageOptions) (domain.Message, error)
	BotAPIStopPollWithOptions(context.Context, int64, int64, int, bool, *domain.MessageReplyMarkup, domain.BotAPIMessageOptions) (domain.MessagePoll, error)
}

func (h *handler) businessEditGateway(w http.ResponseWriter, values map[string]string) (BusinessEditExtraGateway, domain.BotAPIMessageOptions, bool) {
	g, ok := h.gateway.(BusinessEditExtraGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, domain.BotAPIMessageOptions{}, false
	}
	options, err := botAPIMessageOptions(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return nil, options, false
	}
	if values["inline_message_id"] != "" {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_IDENTIFIER_INVALID")
		return nil, options, false
	}
	return g, options, true
}

func init() {
	registerMethods(map[string]methodFunc{
		"editMessageCaption":     (*handler).editMessageCaption,
		"editMessageReplyMarkup": (*handler).editMessageReplyMarkup,
		"editMessageMedia":       (*handler).editMessageMedia,
		"stopPoll":               (*handler).stopPoll,
	})
	registerErrorMarkers("MESSAGE_IDENTIFIER_INVALID", "MESSAGE_AUTHOR_REQUIRED", "EDIT_MESSAGES_FORBIDDEN", "MESSAGE_POLL_CLOSED", "MEDIA_PREV_INVALID")
}

// editMessageMediaKinds 是 editMessageMedia 接受的 InputMedia 类型。
var editMessageMediaKinds = map[string]bool{"animation": true, "audio": true, "document": true, "photo": true, "video": true, "live_photo": true}

func (h *handler) editMessageReplyMarkup(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.editExtraGateway(w)
	if !ok {
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	target, desc := h.parseEditTarget(r.Context(), botID, values)
	if desc != "" {
		writeAPIError(w, http.StatusBadRequest, desc)
		return
	}
	markup, _, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var (
		msg    domain.Message
		result bool
	)
	if values["business_connection_id"] != "" {
		business, options, ok := h.businessEditGateway(w, values)
		if !ok {
			return
		}
		msg, err = business.BotAPIEditMessageReplyMarkupWithOptions(r.Context(), botID, target.chatID, target.messageID, markup, options)
	} else if target.inline {
		result, err = g.BotAPIEditInlineMessageReplyMarkup(r.Context(), botID, target.inlineID, markup)
	} else {
		msg, err = g.BotAPIEditMessageReplyMarkup(r.Context(), botID, target.chatID, target.messageID, markup)
	}
	h.writeEditResult(w, r, botID, target, msg, result, err)
}

func (h *handler) editMessageMedia(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.editExtraGateway(w)
	if !ok {
		return
	}
	values, files, err := requestValuesWithFiles(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	target, desc := h.parseEditTarget(r.Context(), botID, values)
	if desc != "" {
		writeAPIError(w, http.StatusBadRequest, desc)
		return
	}
	if strings.TrimSpace(values["media"]) == "" {
		writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
		return
	}
	item, err := inputMediaFromAPI(json.RawMessage(values["media"]), files)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !editMessageMediaKinds[item.Kind] {
		writeAPIError(w, http.StatusBadRequest, "MEDIA_INVALID")
		return
	}
	markup, setMarkup, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var (
		msg    domain.Message
		result bool
	)
	if values["business_connection_id"] != "" {
		business, options, ok := h.businessEditGateway(w, values)
		if !ok {
			return
		}
		msg, err = business.BotAPIEditMessageMediaWithOptions(r.Context(), botID, target.chatID, target.messageID, item, setMarkup, markup, options)
	} else if target.inline {
		result, err = g.BotAPIEditInlineMessageMedia(r.Context(), botID, target.inlineID, item, setMarkup, markup)
	} else {
		msg, err = g.BotAPIEditMessageMedia(r.Context(), botID, target.chatID, target.messageID, item, setMarkup, markup)
	}
	h.writeEditResult(w, r, botID, target, msg, result, err)
}

func (h *handler) stopPoll(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.editExtraGateway(w)
	if !ok {
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	// stopPoll 没有 inline_message_id 形式。
	delete(values, "inline_message_id")
	target, desc := h.parseEditTarget(r.Context(), botID, values)
	if desc != "" {
		writeAPIError(w, http.StatusBadRequest, desc)
		return
	}
	markup, setMarkup, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var poll domain.MessagePoll
	if values["business_connection_id"] != "" {
		business, options, ok := h.businessEditGateway(w, values)
		if !ok {
			return
		}
		poll, err = business.BotAPIStopPollWithOptions(r.Context(), botID, target.chatID, target.messageID, setMarkup, markup, options)
	} else {
		poll, err = g.BotAPIStopPoll(r.Context(), botID, target.chatID, target.messageID, setMarkup, markup)
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, apiPoll(poll, nil))
}

// editTarget 是编辑方法的消息定位：chat_id+message_id 与 inline_message_id 二选一。
type editTarget struct {
	chatID    int64
	messageID int
	inline    bool
	inlineID  domain.BotInlineMessageID
}

// parseEditTarget 失败时返回的是 API 错误描述。
func (h *handler) parseEditTarget(ctx context.Context, botID int64, values map[string]string) (editTarget, string) {
	rawInline := strings.TrimSpace(values["inline_message_id"])
	if rawInline != "" {
		if strings.TrimSpace(values["chat_id"]) != "" || strings.TrimSpace(values["message_id"]) != "" {
			return editTarget{}, "MESSAGE_IDENTIFIER_INVALID"
		}
		id, err := decodeBotAPIInlineMessageID(rawInline)
		if err != nil {
			return editTarget{}, err.Error()
		}
		return editTarget{inline: true, inlineID: id}, ""
	}
	chatID, err := h.chatIDParam(ctx, botID, values["chat_id"])
	if err != nil {
		return editTarget{}, apiErrorDescription(err)
	}
	messageID := apiInt(values["message_id"], 0)
	if messageID <= 0 {
		return editTarget{}, "MESSAGE_ID_INVALID"
	}
	return editTarget{chatID: chatID, messageID: messageID}, ""
}

func (h *handler) editExtraGateway(w http.ResponseWriter) (EditExtraGateway, bool) {
	g, ok := h.gateway.(EditExtraGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
	}
	return g, ok
}

// writeEditResult 按定位方式输出：inline 消息回 true，其余回编辑后的 Message。
func (h *handler) writeEditResult(w http.ResponseWriter, r *http.Request, botID int64, target editTarget, msg domain.Message, ok bool, err error) {
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	if target.inline {
		writeAPIOK(w, ok)
		return
	}
	h.writeBotMessages(w, r, botID, []domain.Message{msg}, false)
}

func (h *handler) editMessageCaption(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.editExtraGateway(w)
	if !ok {
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	target, desc := h.parseEditTarget(r.Context(), botID, values)
	if desc != "" {
		writeAPIError(w, http.StatusBadRequest, desc)
		return
	}
	caption, entities, err := botAPIFormattedTextRaw(values["caption"], values["parse_mode"], values["caption_entities"], domain.MaxEphemeralCaptionLength, false)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	markup, setMarkup, err := optionalInlineReplyMarkup(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	var (
		msg    domain.Message
		result bool
	)
	if values["business_connection_id"] != "" {
		business, options, ok := h.businessEditGateway(w, values)
		if !ok {
			return
		}
		msg, err = business.BotAPIEditMessageCaptionWithOptions(r.Context(), botID, target.chatID, target.messageID, caption, entities, setMarkup, markup, options)
	} else if target.inline {
		result, err = g.BotAPIEditInlineMessageCaption(r.Context(), botID, target.inlineID, caption, entities, setMarkup, markup)
	} else {
		msg, err = g.BotAPIEditMessageCaption(r.Context(), botID, target.chatID, target.messageID, caption, entities, setMarkup, markup)
	}
	h.writeEditResult(w, r, botID, target, msg, result, err)
}
