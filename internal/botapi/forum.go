package botapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
)

type ForumGateway interface {
	BotAPIGetForumTopicIconStickers(context.Context, int64) ([]domain.Document, error)
	BotAPICreateForumTopic(context.Context, int64, int64, string, int, int64) (domain.ChannelForumTopic, error)
	BotAPIEditForumTopic(context.Context, int64, int64, int, *string, *int64) error
	BotAPISetForumTopicClosed(context.Context, int64, int64, int, bool) error
	BotAPIDeleteForumTopic(context.Context, int64, int64, int) error
	BotAPIUnpinAllForumTopicMessages(context.Context, int64, int64, int) error
	BotAPIEditGeneralForumTopic(context.Context, int64, int64, *string, *bool, *bool) error
}

func init() {
	registerMethods(map[string]methodFunc{
		"getForumTopicIconStickers":         (*handler).getForumTopicIconStickers,
		"createForumTopic":                  (*handler).createForumTopic,
		"editForumTopic":                    (*handler).editForumTopic,
		"closeForumTopic":                   (*handler).closeForumTopic,
		"reopenForumTopic":                  (*handler).reopenForumTopic,
		"deleteForumTopic":                  (*handler).deleteForumTopic,
		"unpinAllForumTopicMessages":        (*handler).unpinAllForumTopicMessages,
		"editGeneralForumTopic":             (*handler).editGeneralForumTopic,
		"closeGeneralForumTopic":            (*handler).closeGeneralForumTopic,
		"reopenGeneralForumTopic":           (*handler).reopenGeneralForumTopic,
		"hideGeneralForumTopic":             (*handler).hideGeneralForumTopic,
		"unhideGeneralForumTopic":           (*handler).unhideGeneralForumTopic,
		"unpinAllGeneralForumTopicMessages": (*handler).unpinAllGeneralForumTopicMessages,
	})
	registerErrorMarkers("TOPIC_ID_INVALID", "TOPIC_TITLE_EMPTY", "CHANNEL_FORUM_MISSING", "TOPIC_NOT_MODIFIED", "ICON_COLOR_INVALID", "CUSTOM_EMOJI_ID_INVALID")
}

func (h *handler) forumRequest(w http.ResponseWriter, r *http.Request, botID int64) (ForumGateway, map[string]string, int64, bool) {
	g, ok := h.gateway.(ForumGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, nil, 0, false
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return nil, nil, 0, false
	}
	chatID, err := h.chatIDParam(r.Context(), botID, values["chat_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return nil, nil, 0, false
	}
	return g, values, chatID, true
}

func forumThreadID(values map[string]string) (int, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(values["message_thread_id"]), 10, 32)
	if err != nil || id <= 0 {
		return 0, errors.New("TOPIC_ID_INVALID")
	}
	return int(id), nil
}

func forumName(name string, required bool) error {
	if required && strings.TrimSpace(name) == "" {
		return errors.New("TOPIC_TITLE_EMPTY")
	}
	if utf8.RuneCountInString(name) > 128 {
		return errors.New("TOPIC_TITLE_EMPTY")
	}
	return nil
}

func forumEmoji(values map[string]string) (*int64, error) {
	text, present := values["icon_custom_emoji_id"]
	if !present {
		return nil, nil
	}
	var id int64
	if text != "" {
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil || parsed <= 0 {
			return nil, errors.New("CUSTOM_EMOJI_ID_INVALID")
		}
		id = parsed
	}
	return &id, nil
}

func forumTopicObject(topic domain.ChannelForumTopic) map[string]any {
	out := map[string]any{"message_thread_id": topic.TopicID, "name": topic.Title, "icon_color": topic.IconColor}
	if topic.IconEmojiID != 0 {
		out["icon_custom_emoji_id"] = strconv.FormatInt(topic.IconEmojiID, 10)
	}
	return out
}

func (h *handler) getForumTopicIconStickers(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.gateway.(ForumGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	docs, err := g.BotAPIGetForumTopicIconStickers(r.Context(), botID)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	out := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		sticker := apiSticker(doc)
		sticker["type"] = "custom_emoji"
		sticker["custom_emoji_id"] = strconv.FormatInt(doc.ID, 10)
		sticker["is_animated"] = doc.MimeType == "application/x-tgsticker"
		sticker["is_video"] = doc.MimeType == "video/webm"
		for _, attr := range doc.Attributes {
			if attr.Kind == domain.DocAttrImageSize || attr.Kind == domain.DocAttrVideo {
				sticker["width"], sticker["height"] = attr.W, attr.H
			}
			if attr.Kind == domain.DocAttrCustomEmoji {
				sticker["emoji"] = attr.Alt
				if attr.TextColor {
					sticker["needs_repainting"] = true
				}
			}
		}
		out = append(out, sticker)
	}
	writeAPIOK(w, out)
}

func (h *handler) createForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, chat, ok := h.forumRequest(w, r, botID)
	if !ok {
		return
	}
	if err := forumName(v["name"], true); err != nil {
		writeChatAdminResult(w, err)
		return
	}
	color := domain.DefaultForumTopicIconColor
	if raw, present := v["icon_color"]; present {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			writeChatAdminResult(w, errors.New("ICON_COLOR_INVALID"))
			return
		}
		color = int(n)
	}
	switch color {
	case 7322096, 16766590, 13338331, 9367192, 16749490, 16478047:
	default:
		writeChatAdminResult(w, errors.New("ICON_COLOR_INVALID"))
		return
	}
	emoji, err := forumEmoji(v)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	var emojiID int64
	if emoji != nil {
		emojiID = *emoji
	}
	topic, err := g.BotAPICreateForumTopic(r.Context(), botID, chat, v["name"], color, emojiID)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	writeAPIOK(w, forumTopicObject(topic))
}

func (h *handler) editForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, chat, ok := h.forumRequest(w, r, botID)
	if !ok {
		return
	}
	id, err := forumThreadID(v)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	var name *string
	if text := v["name"]; text != "" {
		if err := forumName(text, false); err != nil {
			writeChatAdminResult(w, err)
			return
		}
		name = &text
	}
	emoji, err := forumEmoji(v)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	writeChatAdminResult(w, g.BotAPIEditForumTopic(r.Context(), botID, chat, id, name, emoji))
}

func (h *handler) closeForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	h.setForumTopicClosed(w, r, botID, true)
}
func (h *handler) reopenForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	h.setForumTopicClosed(w, r, botID, false)
}
func (h *handler) setForumTopicClosed(w http.ResponseWriter, r *http.Request, botID int64, closed bool) {
	g, v, chat, ok := h.forumRequest(w, r, botID)
	if !ok {
		return
	}
	id, err := forumThreadID(v)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	writeChatAdminResult(w, g.BotAPISetForumTopicClosed(r.Context(), botID, chat, id, closed))
}
func (h *handler) deleteForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, chat, ok := h.forumRequest(w, r, botID)
	if !ok {
		return
	}
	id, err := forumThreadID(v)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	writeChatAdminResult(w, g.BotAPIDeleteForumTopic(r.Context(), botID, chat, id))
}
func (h *handler) unpinAllForumTopicMessages(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, chat, ok := h.forumRequest(w, r, botID)
	if !ok {
		return
	}
	id, err := forumThreadID(v)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	writeChatAdminResult(w, g.BotAPIUnpinAllForumTopicMessages(r.Context(), botID, chat, id))
}
func (h *handler) unpinAllGeneralForumTopicMessages(w http.ResponseWriter, r *http.Request, botID int64) {
	g, _, chat, ok := h.forumRequest(w, r, botID)
	if !ok {
		return
	}
	if chat >= 0 {
		writeChatAdminResult(w, errors.New("CHANNEL_FORUM_MISSING"))
		return
	}
	writeChatAdminResult(w, g.BotAPIUnpinAllForumTopicMessages(r.Context(), botID, chat, domain.ForumGeneralTopicID))
}
func (h *handler) editGeneralForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, chat, ok := h.forumRequest(w, r, botID)
	if !ok {
		return
	}
	name := v["name"]
	if err := forumName(name, true); err != nil {
		writeChatAdminResult(w, err)
		return
	}
	writeChatAdminResult(w, g.BotAPIEditGeneralForumTopic(r.Context(), botID, chat, &name, nil, nil))
}
func (h *handler) closeGeneralForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	closed := true
	h.setGeneralForumTopic(w, r, botID, &closed, nil)
}
func (h *handler) reopenGeneralForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	closed := false
	h.setGeneralForumTopic(w, r, botID, &closed, nil)
}
func (h *handler) hideGeneralForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	hidden := true
	h.setGeneralForumTopic(w, r, botID, nil, &hidden)
}
func (h *handler) unhideGeneralForumTopic(w http.ResponseWriter, r *http.Request, botID int64) {
	hidden := false
	h.setGeneralForumTopic(w, r, botID, nil, &hidden)
}
func (h *handler) setGeneralForumTopic(w http.ResponseWriter, r *http.Request, botID int64, closed, hidden *bool) {
	g, _, chat, ok := h.forumRequest(w, r, botID)
	if !ok {
		return
	}
	writeChatAdminResult(w, g.BotAPIEditGeneralForumTopic(r.Context(), botID, chat, nil, closed, hidden))
}
