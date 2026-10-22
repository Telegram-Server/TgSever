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

type ChatInvitesGateway interface {
	BotAPISetChatSenderBan(context.Context, int64, int64, int64, bool) error
	BotAPISetChatStickerSet(context.Context, int64, int64, string) error
	BotAPISetChatMemberTag(context.Context, int64, int64, int64, string) error
	BotAPIExportChatInviteLink(context.Context, int64, int64) (domain.ChannelInvite, error)
	BotAPICreateChatInviteLink(context.Context, int64, int64, domain.ExportChannelInviteRequest) (domain.ChannelInvite, error)
	BotAPIEditChatInviteLink(context.Context, int64, int64, string, domain.EditChannelInviteRequest) (domain.ChannelInvite, error)
	BotAPIRevokeChatInviteLink(context.Context, int64, int64, string) (domain.ChannelInvite, error)
	BotAPIProcessChatJoinRequest(context.Context, int64, int64, int64, bool) error
	BotAPIAnswerChatJoinRequestQuery(context.Context, int64, string, string) error
	BotAPISendChatJoinRequestWebApp(context.Context, int64, string, string) error
}

func init() {
	registerMethods(map[string]methodFunc{
		"banChatSenderChat":          (*handler).banChatSenderChat,
		"unbanChatSenderChat":        (*handler).unbanChatSenderChat,
		"setChatStickerSet":          (*handler).setChatStickerSet,
		"deleteChatStickerSet":       (*handler).deleteChatStickerSet,
		"setChatMemberTag":           (*handler).setChatMemberTag,
		"exportChatInviteLink":       (*handler).exportChatInviteLink,
		"createChatInviteLink":       (*handler).createChatInviteLink,
		"editChatInviteLink":         (*handler).editChatInviteLink,
		"revokeChatInviteLink":       (*handler).revokeChatInviteLink,
		"approveChatJoinRequest":     (*handler).approveChatJoinRequest,
		"declineChatJoinRequest":     (*handler).declineChatJoinRequest,
		"answerChatJoinRequestQuery": (*handler).answerChatJoinRequestQuery,
		"sendChatJoinRequestWebApp":  (*handler).sendChatJoinRequestWebApp,
	})
	registerErrorMarkers("INVITE_HASH_INVALID", "INVITE_HASH_EXPIRED", "INVITE_REVOKED_MISSING", "HIDE_REQUESTER_MISSING", "STICKERSET_INVALID", "PARTICIPANTS_TOO_FEW", "ADMIN_RANK_EMOJI_NOT_ALLOWED", "QUERY_ID_INVALID", "QUERY_RESULT_INVALID", "INVITE_LINK_NOT_OWNED", "INVITE_LINK_IS_PRIMARY")
}

func (h *handler) chatInvitesRequest(w http.ResponseWriter, r *http.Request) (ChatInvitesGateway, map[string]string, bool) {
	g, ok := h.gateway.(ChatInvitesGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, nil, false
	}
	v, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return nil, nil, false
	}
	return g, v, true
}

func (h *handler) chatInvitesChat(w http.ResponseWriter, r *http.Request, botID int64) (ChatInvitesGateway, map[string]string, int64, bool) {
	g, v, ok := h.chatInvitesRequest(w, r)
	if !ok {
		return nil, nil, 0, false
	}
	id, err := h.chatIDParam(r.Context(), botID, v["chat_id"])
	if err != nil {
		writeChatAdminResult(w, err)
		return nil, nil, 0, false
	}
	return g, v, id, true
}

func (h *handler) banChatSenderChat(w http.ResponseWriter, r *http.Request, botID int64) {
	h.chatSenderBan(w, r, botID, true)
}
func (h *handler) unbanChatSenderChat(w http.ResponseWriter, r *http.Request, botID int64) {
	h.chatSenderBan(w, r, botID, false)
}
func (h *handler) chatSenderBan(w http.ResponseWriter, r *http.Request, botID int64, banned bool) {
	g, v, id, ok := h.chatInvitesChat(w, r, botID)
	if !ok {
		return
	}
	sender, err := strconv.ParseInt(v["sender_chat_id"], 10, 64)
	if err != nil || sender >= 0 {
		writeChatAdminResult(w, errors.New("CHAT_ID_INVALID"))
		return
	}
	writeChatAdminResult(w, g.BotAPISetChatSenderBan(r.Context(), botID, id, sender, banned))
}
func (h *handler) setChatStickerSet(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, id, ok := h.chatInvitesChat(w, r, botID)
	if !ok {
		return
	}
	if strings.TrimSpace(v["sticker_set_name"]) == "" {
		writeChatAdminResult(w, errors.New("STICKERSET_INVALID"))
		return
	}
	writeChatAdminResult(w, g.BotAPISetChatStickerSet(r.Context(), botID, id, v["sticker_set_name"]))
}
func (h *handler) deleteChatStickerSet(w http.ResponseWriter, r *http.Request, botID int64) {
	g, _, id, ok := h.chatInvitesChat(w, r, botID)
	if !ok {
		return
	}
	writeChatAdminResult(w, g.BotAPISetChatStickerSet(r.Context(), botID, id, ""))
}
func (h *handler) setChatMemberTag(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, id, ok := h.chatInvitesChat(w, r, botID)
	if !ok {
		return
	}
	user, ok := chatAdminUserID(w, v)
	if !ok {
		return
	}
	if err := domain.ValidateChannelRank(v["tag"]); err != nil {
		writeChatAdminResult(w, errors.New("ADMIN_RANK_INVALID"))
		return
	}
	writeChatAdminResult(w, g.BotAPISetChatMemberTag(r.Context(), botID, id, user, v["tag"]))
}
func (h *handler) writeChatInvite(w http.ResponseWriter, r *http.Request, botID int64, invite domain.ChannelInvite, err error, primary bool) {
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	if primary {
		writeAPIOK(w, invite.Link)
		return
	}
	creator, err := h.gateway.BotAPISelf(r.Context(), botID)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	writeAPIOK(w, apiInviteLink(invite, creator, botID))
}
func (h *handler) exportChatInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	g, _, id, ok := h.chatInvitesChat(w, r, botID)
	if !ok {
		return
	}
	invite, err := g.BotAPIExportChatInviteLink(r.Context(), botID, id)
	h.writeChatInvite(w, r, botID, invite, err, true)
}
func chatInviteOptions(v map[string]string) (domain.EditChannelInviteRequest, error) {
	var p domain.EditChannelInviteRequest
	if p.Title, p.HasTitle = v["name"]; p.HasTitle && (!utf8.ValidString(p.Title) || utf8.RuneCountInString(p.Title) > 32) {
		return p, errors.New("BAD_REQUEST")
	}
	if raw, ok := v["expire_date"]; ok {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 0 {
			return p, errors.New("BAD_REQUEST")
		}
		p.HasExpireDate = true
		p.ExpireDate = n
	}
	if raw, ok := v["member_limit"]; ok {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 99999 {
			return p, errors.New("BAD_REQUEST")
		}
		p.HasUsageLimit = true
		p.UsageLimit = n
	}
	if raw, ok := v["creates_join_request"]; ok {
		n, e := strconv.ParseBool(raw)
		if e != nil {
			return p, errors.New("BAD_REQUEST")
		}
		p.HasRequestNeeded = true
		p.RequestNeeded = n
	}
	if p.RequestNeeded && p.HasUsageLimit {
		return p, errors.New("BAD_REQUEST")
	}
	return p, nil
}
func (h *handler) createChatInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, id, ok := h.chatInvitesChat(w, r, botID)
	if !ok {
		return
	}
	p, err := chatInviteOptions(v)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	invite, err := g.BotAPICreateChatInviteLink(r.Context(), botID, id, domain.ExportChannelInviteRequest{Title: p.Title, ExpireDate: p.ExpireDate, UsageLimit: p.UsageLimit, RequestNeeded: p.RequestNeeded})
	h.writeChatInvite(w, r, botID, invite, err, false)
}
func (h *handler) editChatInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, id, ok := h.chatInvitesChat(w, r, botID)
	if !ok {
		return
	}
	p, err := chatInviteOptions(v)
	if err != nil {
		writeChatAdminResult(w, err)
		return
	}
	invite, err := g.BotAPIEditChatInviteLink(r.Context(), botID, id, v["invite_link"], p)
	h.writeChatInvite(w, r, botID, invite, err, false)
}
func (h *handler) revokeChatInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, id, ok := h.chatInvitesChat(w, r, botID)
	if !ok {
		return
	}
	invite, err := g.BotAPIRevokeChatInviteLink(r.Context(), botID, id, v["invite_link"])
	h.writeChatInvite(w, r, botID, invite, err, false)
}
func (h *handler) approveChatJoinRequest(w http.ResponseWriter, r *http.Request, botID int64) {
	h.processChatJoinRequest(w, r, botID, true)
}
func (h *handler) declineChatJoinRequest(w http.ResponseWriter, r *http.Request, botID int64) {
	h.processChatJoinRequest(w, r, botID, false)
}
func (h *handler) processChatJoinRequest(w http.ResponseWriter, r *http.Request, botID int64, approved bool) {
	g, v, id, ok := h.chatInvitesChat(w, r, botID)
	if !ok {
		return
	}
	user, ok := chatAdminUserID(w, v)
	if !ok {
		return
	}
	writeChatAdminResult(w, g.BotAPIProcessChatJoinRequest(r.Context(), botID, id, user, approved))
}
func (h *handler) answerChatJoinRequestQuery(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.chatInvitesRequest(w, r)
	if !ok {
		return
	}
	writeChatAdminResult(w, g.BotAPIAnswerChatJoinRequestQuery(r.Context(), botID, v["chat_join_request_query_id"], v["result"]))
}
func (h *handler) sendChatJoinRequestWebApp(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, ok := h.chatInvitesRequest(w, r)
	if !ok {
		return
	}
	writeChatAdminResult(w, g.BotAPISendChatJoinRequestWebApp(r.Context(), botID, v["chat_join_request_query_id"], v["web_app_url"]))
}
