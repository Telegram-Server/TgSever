package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"telesrv/internal/domain"
)

// ForwardGateway 承载 forwardMessage(s) / copyMessage(s)。可选接口：网关不实现时
// 这组方法回 501 METHOD_NOT_FOUND。chatID/fromChatID 都是 Bot API chat_id。
type ForwardGateway interface {
	// BotAPIForwardMessages 以机器人身份转发，返回目标会话里新建的消息（按源 id 升序）。
	BotAPIForwardMessages(ctx context.Context, botID, chatID, fromChatID int64, messageIDs []int, silent, protectContent bool) ([]domain.Message, error)
	// BotAPICopyMessages 复制（不带转发头）；removeCaption 去掉媒体说明文字。
	BotAPICopyMessages(ctx context.Context, botID, chatID, fromChatID int64, messageIDs []int, removeCaption, silent, protectContent bool) ([]domain.Message, error)
	// BotAPICopyMessageWithOptions copies one message with the shared reply and send options.
	BotAPICopyMessageWithOptions(ctx context.Context, botID, chatID, fromChatID int64, messageID int, setCaption bool, caption string, entities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int, options domain.BotAPIMessageOptions) (domain.Message, error)
}

// maxForwardMessageIDs 是 forwardMessages/copyMessages 一次最多处理的消息数。
const maxForwardMessageIDs = 100

func init() {
	registerMethods(map[string]methodFunc{
		"forwardMessage":  (*handler).forwardMessage,
		"forwardMessages": (*handler).forwardMessages,
		"copyMessage":     (*handler).copyMessage,
		"copyMessages":    (*handler).copyMessages,
	})
	registerErrorMarkers("MESSAGE_IDS_EMPTY", "CHAT_FORWARDS_RESTRICTED", "PEER_ID_INVALID")
	registerMessageProjection(apiForwardOrigin)
}

// forwardTarget 解析转发/复制共用的 chat_id 与 from_chat_id。
func (h *handler) forwardTarget(ctx context.Context, botID int64, values map[string]string) (chatID, fromChatID int64, err error) {
	if chatID, err = h.chatIDParam(ctx, botID, values["chat_id"]); err != nil {
		return 0, 0, err
	}
	if fromChatID, err = h.chatIDParam(ctx, botID, values["from_chat_id"]); err != nil {
		return 0, 0, err
	}
	return chatID, fromChatID, nil
}

func (h *handler) forwardGateway(w http.ResponseWriter) (ForwardGateway, bool) {
	g, ok := h.gateway.(ForwardGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
	}
	return g, ok
}

func (h *handler) forwardMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.forwardGateway(w)
	if !ok {
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, fromChatID, err := h.forwardTarget(r.Context(), botID, values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	messageID := apiInt(values["message_id"], 0)
	if messageID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return
	}
	msgs, err := g.BotAPIForwardMessages(r.Context(), botID, chatID, fromChatID, []int{messageID},
		apiBool(values["disable_notification"]), apiBool(values["protect_content"]))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	if len(msgs) == 0 {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return
	}
	h.writeSelfMessage(w, r, botID, msgs[0])
}

// copyMessage 返回 MessageId。caption 键出现即替换原说明文字（空串等于去掉），
// 不出现则保留原文。
func (h *handler) copyMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.forwardGateway(w)
	if !ok {
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, fromChatID, err := h.forwardTarget(r.Context(), botID, values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	messageID := apiInt(values["message_id"], 0)
	if messageID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return
	}
	_, setCaption := values["caption"]
	var (
		caption  string
		entities []domain.MessageEntity
	)
	if setCaption {
		caption, entities, err = botAPIFormattedTextRaw(values["caption"], values["parse_mode"], values["caption_entities"], domain.MaxMessageTextLength, false)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var markup *domain.MessageReplyMarkup
	if raw := strings.TrimSpace(values["reply_markup"]); raw != "" {
		markup, err = replyMarkupFromAPI(json.RawMessage(raw))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	options, err := botAPIMessageOptions(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	replyTo := 0
	if options.ReplyParameters != nil {
		replyTo = options.ReplyParameters.MessageID
	}
	msg, err := g.BotAPICopyMessageWithOptions(r.Context(), botID, chatID, fromChatID, messageID, setCaption, caption, entities, markup,
		apiBool(values["disable_notification"]), options.ProtectContent, replyTo, options)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, map[string]any{"message_id": msg.ID})
}

func (h *handler) forwardMessages(w http.ResponseWriter, r *http.Request, botID int64) {
	h.forwardMessageBatch(w, r, botID, false)
}

func (h *handler) copyMessages(w http.ResponseWriter, r *http.Request, botID int64) {
	h.forwardMessageBatch(w, r, botID, true)
}

// forwardMessageBatch 是 forwardMessages 与 copyMessages 的共同实现，返回 MessageId 数组。
func (h *handler) forwardMessageBatch(w http.ResponseWriter, r *http.Request, botID int64, copyMode bool) {
	g, ok := h.forwardGateway(w)
	if !ok {
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, fromChatID, err := h.forwardTarget(r.Context(), botID, values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	ids, err := forwardMessageIDs(values["message_ids"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	silent, protect := apiBool(values["disable_notification"]), apiBool(values["protect_content"])
	var msgs []domain.Message
	if copyMode {
		msgs, err = g.BotAPICopyMessages(r.Context(), botID, chatID, fromChatID, ids, apiBool(values["remove_caption"]), silent, protect)
	} else {
		msgs, err = g.BotAPIForwardMessages(r.Context(), botID, chatID, fromChatID, ids, silent, protect)
	}
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, apiMessageIDs(msgs))
}

// forwardMessageIDs 解析 message_ids：1–100 个正整数，去重后按升序处理。
func forwardMessageIDs(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("MESSAGE_IDS_EMPTY")
	}
	var ids []int
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, errors.New("MESSAGE_ID_INVALID")
	}
	if len(ids) == 0 {
		return nil, errors.New("MESSAGE_IDS_EMPTY")
	}
	if len(ids) > maxForwardMessageIDs {
		return nil, errors.New("MESSAGE_ID_INVALID")
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, errors.New("MESSAGE_ID_INVALID")
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// apiMessageIDs 投影成官方 MessageId 数组。
func apiMessageIDs(msgs []domain.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, map[string]any{"message_id": msg.ID})
	}
	return out
}

// writeSelfMessage 与 sendMessage 相同：带上机器人自己的 User 投影 Message。
func (h *handler) writeSelfMessage(w http.ResponseWriter, r *http.Request, botID int64, msg domain.Message) {
	users := []domain.User(nil)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	writeAPIOK(w, apiMessage(msg, users))
}

// apiForwardOrigin 把转发头投影成官方 MessageOrigin（forward_origin）。
func apiForwardOrigin(msg domain.Message, out map[string]any) {
	fwd := msg.Forward
	if fwd == nil {
		return
	}
	origin := map[string]any{"date": fwd.Date}
	switch {
	case fwd.From.Type == domain.PeerTypeUser && fwd.From.ID != 0:
		origin["type"] = "user"
		origin["sender_user"] = apiUser(domain.User{ID: fwd.From.ID})
	case fwd.From.Type == domain.PeerTypeChannel && fwd.From.ID != 0:
		chat := apiChat(fwd.From, nil)
		if fwd.ChannelPost > 0 {
			chat["type"] = "channel"
			origin["type"] = "channel"
			origin["chat"] = chat
			origin["message_id"] = fwd.ChannelPost
		} else {
			origin["type"] = "chat"
			origin["sender_chat"] = chat
		}
	case strings.TrimSpace(fwd.FromName) != "":
		origin["type"] = "hidden_user"
		origin["sender_user_name"] = fwd.FromName
	default:
		return
	}
	out["forward_origin"] = origin
}
