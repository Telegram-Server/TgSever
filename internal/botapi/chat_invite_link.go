package botapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
)

// ChatInviteLinkGateway 是邀请链接方法（create/edit/revoke/exportChatInviteLink）的可选
// 网关。生产实现以机器人身份调用 messages.exportChatInvite / editExportedChatInvite，
// 权限（群主，或拥有 InviteUsers / ChangeInfo 的管理员）在处理器与 store 里判定。
type ChatInviteLinkGateway interface {
	BotAPICreateChatInviteLink(ctx context.Context, botID, chatID int64, options domain.BotAPIChatInviteLinkOptions) (domain.BotAPIChatInviteLink, error)
	// link 是调用方传入的完整链接；options 里只有传入的字段非 nil。
	BotAPIEditChatInviteLink(ctx context.Context, botID, chatID int64, link string, options domain.BotAPIChatInviteLinkOptions) (domain.BotAPIChatInviteLink, error)
	BotAPIRevokeChatInviteLink(ctx context.Context, botID, chatID int64, link string) (domain.BotAPIChatInviteLink, error)
	// BotAPIExportChatInviteLink 撤销机器人当前的主链接并生成新的主链接，返回新链接。
	BotAPIExportChatInviteLink(ctx context.Context, botID, chatID int64) (string, error)
}

// chatInviteLinkErrorMarkers 是邀请链接处理器会返回、而核心白名单里没有的 TL 错误码。
var chatInviteLinkErrorMarkers = []string{
	"INVITE_HASH_EMPTY",
	"INVITE_HASH_INVALID",
	"INVITE_HASH_EXPIRED",
	"INVITE_REVOKED_MISSING",
	"CHAT_INVITE_PERMANENT",
	"LIMIT_INVALID",
}

func init() {
	registerMethods(map[string]methodFunc{
		"createChatInviteLink": (*handler).createChatInviteLink,
		"editChatInviteLink":   (*handler).editChatInviteLink,
		"revokeChatInviteLink": (*handler).revokeChatInviteLink,
		"exportChatInviteLink": (*handler).exportChatInviteLink,
	})
	registerErrorMarkers(chatInviteLinkErrorMarkers...)
}

// chatInviteLinkErrorDescription 把邀请链接方法的失败映射成官方 Bot API 的说明文本；
// 其余错误保持网关错误码。
func chatInviteLinkErrorDescription(err error) string {
	switch code := apiErrorDescription(err); code {
	case "CHAT_ADMIN_REQUIRED":
		return "Bad Request: not enough rights to manage chat invite links"
	default:
		return code
	}
}

// apiChatInviteLink 投影官方 ChatInviteLink 对象；零值的可选字段按官方省略。
// chat_join_request 更新里的 invite_link 也复用它。
func apiChatInviteLink(link domain.BotAPIChatInviteLink) map[string]any {
	creator := link.Creator
	if creator.ID == 0 {
		creator.ID = link.Invite.AdminUserID
	}
	invite := link.Invite
	out := map[string]any{
		"invite_link":          link.Link,
		"creator":              apiUser(creator),
		"creates_join_request": invite.RequestNeeded,
		"is_primary":           invite.Permanent,
		"is_revoked":           invite.Revoked,
	}
	if invite.Title != "" {
		out["name"] = invite.Title
	}
	if invite.ExpireDate > 0 {
		out["expire_date"] = invite.ExpireDate
	}
	if invite.UsageLimit > 0 {
		out["member_limit"] = invite.UsageLimit
	}
	if invite.RequestedCount > 0 {
		out["pending_join_request_count"] = invite.RequestedCount
	}
	return out
}

// chatInviteLinkRequest 取出网关、参数与 chat_id；失败时已写好响应。
func (h *handler) chatInviteLinkRequest(w http.ResponseWriter, r *http.Request, botID int64) (ChatInviteLinkGateway, map[string]string, int64, bool) {
	gateway, ok := h.gateway.(ChatInviteLinkGateway)
	if !ok || h.gateway == nil {
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
	return gateway, values, chatID, true
}

// chatInviteLinkParam 取出必填的 invite_link；失败时已写好响应。
func chatInviteLinkParam(w http.ResponseWriter, values map[string]string) (string, bool) {
	link := strings.TrimSpace(values["invite_link"])
	if link == "" {
		writeAPIError(w, http.StatusBadRequest, "Bad Request: invite_link is required")
		return "", false
	}
	return link, true
}

// chatInviteLinkOptions 解析并校验可选字段：只有请求里出现的参数才非 nil，这样 edit
// 才能只改传入的字段。返回的 description 非空表示参数错误。
func chatInviteLinkOptions(values map[string]string) (domain.BotAPIChatInviteLinkOptions, string) {
	var options domain.BotAPIChatInviteLinkOptions
	present := func(key string) (string, bool) {
		raw, ok := values[key]
		raw = strings.TrimSpace(raw)
		return raw, ok && raw != "null"
	}
	if _, ok := present("name"); ok {
		// 名字按原样保存（不 trim），长度按字符而不是字节计。
		name := values["name"]
		if utf8.RuneCountInString(name) > domain.BotAPIInviteLinkNameMaxLength {
			return options, "Bad Request: invite link name is too long"
		}
		options.Name = &name
	}
	if raw, ok := present("expire_date"); ok {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return options, "Bad Request: invalid expire_date specified"
		}
		options.ExpireDate = &v
	}
	if raw, ok := present("member_limit"); ok {
		v, err := strconv.Atoi(raw)
		if err != nil || v < domain.BotAPIInviteLinkMemberLimitMin || v > domain.BotAPIInviteLinkMemberLimitMax {
			return options, "Bad Request: member_limit must be between 1 and 99999"
		}
		options.MemberLimit = &v
	}
	if raw, ok := present("creates_join_request"); ok {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return options, "Bad Request: invalid creates_join_request specified"
		}
		options.CreatesJoinRequest = &v
	}
	if options.MemberLimit != nil && options.CreatesJoinRequest != nil && *options.CreatesJoinRequest {
		return options, "Bad Request: member limit can't be specified for links requiring administrator approval"
	}
	return options, ""
}

func writeChatInviteLinkResult(w http.ResponseWriter, link domain.BotAPIChatInviteLink, err error) {
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, chatInviteLinkErrorDescription(err))
		return
	}
	writeAPIOK(w, apiChatInviteLink(link))
}

func (h *handler) createChatInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatInviteLinkRequest(w, r, botID)
	if !ok {
		return
	}
	options, invalid := chatInviteLinkOptions(values)
	if invalid != "" {
		writeAPIError(w, http.StatusBadRequest, invalid)
		return
	}
	link, err := gateway.BotAPICreateChatInviteLink(r.Context(), botID, chatID, options)
	writeChatInviteLinkResult(w, link, err)
}

func (h *handler) editChatInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatInviteLinkRequest(w, r, botID)
	if !ok {
		return
	}
	inviteLink, ok := chatInviteLinkParam(w, values)
	if !ok {
		return
	}
	options, invalid := chatInviteLinkOptions(values)
	if invalid != "" {
		writeAPIError(w, http.StatusBadRequest, invalid)
		return
	}
	link, err := gateway.BotAPIEditChatInviteLink(r.Context(), botID, chatID, inviteLink, options)
	writeChatInviteLinkResult(w, link, err)
}

func (h *handler) revokeChatInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatInviteLinkRequest(w, r, botID)
	if !ok {
		return
	}
	inviteLink, ok := chatInviteLinkParam(w, values)
	if !ok {
		return
	}
	link, err := gateway.BotAPIRevokeChatInviteLink(r.Context(), botID, chatID, inviteLink)
	writeChatInviteLinkResult(w, link, err)
}

func (h *handler) exportChatInviteLink(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, _, chatID, ok := h.chatInviteLinkRequest(w, r, botID)
	if !ok {
		return
	}
	link, err := gateway.BotAPIExportChatInviteLink(r.Context(), botID, chatID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, chatInviteLinkErrorDescription(err))
		return
	}
	writeAPIOK(w, link)
}
