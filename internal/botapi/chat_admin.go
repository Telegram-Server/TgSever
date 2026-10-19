package botapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"telesrv/internal/domain"
)

// ChatAdminGateway 是群管理方法（封禁/限制/提权/置顶/群资料/退群）的可选网关。
// 生产实现以机器人身份调用对应的 MTProto 处理器，权限校验与更新扇出都在那里。
type ChatAdminGateway interface {
	BotAPIBanChatMember(ctx context.Context, botID, chatID, userID int64, untilDate int, revokeMessages bool) error
	BotAPIUnbanChatMember(ctx context.Context, botID, chatID, userID int64, onlyIfBanned bool) error
	// rights 已含 until_date。
	BotAPIRestrictChatMember(ctx context.Context, botID, chatID, userID int64, rights domain.ChannelBannedRights) error
	// 权限全为 false 即撤销管理员。
	BotAPIPromoteChatMember(ctx context.Context, botID, chatID, userID int64, rights domain.ChannelAdminRights) error
	BotAPISetChatAdministratorCustomTitle(ctx context.Context, botID, chatID, userID int64, title string) error
	BotAPISetChatPermissions(ctx context.Context, botID, chatID int64, rights domain.ChannelBannedRights) error
	BotAPIPinChatMessage(ctx context.Context, botID, chatID int64, messageID int, silent bool) error
	// messageID 为 0 时取消最近一条置顶。
	BotAPIUnpinChatMessage(ctx context.Context, botID, chatID int64, messageID int) error
	BotAPIUnpinAllChatMessages(ctx context.Context, botID, chatID int64) error
	BotAPISetChatTitle(ctx context.Context, botID, chatID int64, title string) error
	BotAPISetChatDescription(ctx context.Context, botID, chatID int64, description string) error
	// data 是 multipart 上传的图片原始字节。
	BotAPISetChatPhoto(ctx context.Context, botID, chatID int64, fileName, mimeType string, data []byte) error
	BotAPIDeleteChatPhoto(ctx context.Context, botID, chatID int64) error
	BotAPILeaveChat(ctx context.Context, botID, chatID int64) error
}

// chatAdminErrorMarkers 是群管理处理器会返回、而核心白名单里没有的 TL 错误码。
var chatAdminErrorMarkers = []string{
	"USER_ADMIN_INVALID",
	"USER_NOT_ADMIN",
	"RIGHT_FORBIDDEN",
	"USER_NOT_PARTICIPANT",
	"PARTICIPANT_ID_INVALID",
	"PEER_ID_INVALID",
	"USER_CREATOR",
	"CHAT_NOT_MODIFIED",
	"CHAT_TITLE_EMPTY",
	"CHAT_ABOUT_TOO_LONG",
	"CHAT_ABOUT_NOT_MODIFIED",
	"ADMIN_RANK_INVALID",
	"ADMINS_TOO_MUCH",
	"PHOTO_INVALID",
	"CHANNEL_INVALID",
	"CHANNEL_PRIVATE",
	"USER_BANNED_IN_CHANNEL",
	"MEGAGROUP_ID_INVALID",
	"FROZEN_METHOD_INVALID",
}

func init() {
	registerMethods(map[string]methodFunc{
		"banChatMember":                   (*handler).banChatMember,
		"unbanChatMember":                 (*handler).unbanChatMember,
		"restrictChatMember":              (*handler).restrictChatMember,
		"promoteChatMember":               (*handler).promoteChatMember,
		"setChatAdministratorCustomTitle": (*handler).setChatAdministratorCustomTitle,
		"setChatPermissions":              (*handler).setChatPermissions,
		"pinChatMessage":                  (*handler).pinChatMessage,
		"unpinChatMessage":                (*handler).unpinChatMessage,
		"unpinAllChatMessages":            (*handler).unpinAllChatMessages,
		"setChatTitle":                    (*handler).setChatTitle,
		"setChatDescription":              (*handler).setChatDescription,
		"setChatPhoto":                    (*handler).setChatPhoto,
		"deleteChatPhoto":                 (*handler).deleteChatPhoto,
		"leaveChat":                       (*handler).leaveChat,
	})
	registerErrorMarkers(chatAdminErrorMarkers...)
}

// chatAdminRequest 解析群管理方法共有的部分：网关、参数与 chat_id。失败时已写好响应。
func (h *handler) chatAdminRequest(w http.ResponseWriter, r *http.Request, botID int64) (ChatAdminGateway, map[string]string, int64, bool) {
	gateway, values, _, chatID, ok := h.chatAdminRequestWithFiles(w, r, botID)
	return gateway, values, chatID, ok
}

// chatAdminRequestWithFiles 同 chatAdminRequest，另外返回 multipart 上传的文件。
func (h *handler) chatAdminRequestWithFiles(w http.ResponseWriter, r *http.Request, botID int64) (ChatAdminGateway, map[string]string, map[string]uploadedFile, int64, bool) {
	gateway, ok := h.gateway.(ChatAdminGateway)
	if !ok || h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, nil, nil, 0, false
	}
	values, files, err := requestValuesWithFiles(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return nil, nil, nil, 0, false
	}
	chatID, err := h.chatIDParam(r.Context(), botID, values["chat_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return nil, nil, nil, 0, false
	}
	return gateway, values, files, chatID, true
}

// chatAdminUserID 解析 user_id；失败时已写好响应。
func chatAdminUserID(w http.ResponseWriter, values map[string]string) (int64, bool) {
	userID, err := strconv.ParseInt(strings.TrimSpace(values["user_id"]), 10, 64)
	if err != nil || userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "USER_ID_INVALID")
		return 0, false
	}
	return userID, true
}

// writeChatAdminResult 把网关结果写成官方的 True 或错误。
func writeChatAdminResult(w http.ResponseWriter, err error) {
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}

func untilDateParam(values map[string]string) int {
	return normalizeUntilDate(apiInt(values["until_date"], 0), int(time.Now().Unix()))
}

func (h *handler) banChatMember(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	userID, ok := chatAdminUserID(w, values)
	if !ok {
		return
	}
	writeChatAdminResult(w, gateway.BotAPIBanChatMember(r.Context(), botID, chatID, userID, untilDateParam(values), apiBool(values["revoke_messages"])))
}

func (h *handler) unbanChatMember(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	userID, ok := chatAdminUserID(w, values)
	if !ok {
		return
	}
	writeChatAdminResult(w, gateway.BotAPIUnbanChatMember(r.Context(), botID, chatID, userID, apiBool(values["only_if_banned"])))
}

// chatPermissionsParam 解析必填的 permissions 与 use_independent_chat_permissions；
// 失败时已写好响应。
func chatPermissionsParam(w http.ResponseWriter, values map[string]string) (domain.ChannelBannedRights, bool) {
	permissions, err := parseChatPermissions(values["permissions"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return domain.ChannelBannedRights{}, false
	}
	return chatPermissionsToBanned(permissions, apiBool(values["use_independent_chat_permissions"])), true
}

func (h *handler) restrictChatMember(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	userID, ok := chatAdminUserID(w, values)
	if !ok {
		return
	}
	rights, ok := chatPermissionsParam(w, values)
	if !ok {
		return
	}
	rights.UntilDate = untilDateParam(values)
	writeChatAdminResult(w, gateway.BotAPIRestrictChatMember(r.Context(), botID, chatID, userID, rights))
}

func (h *handler) promoteChatMember(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	userID, ok := chatAdminUserID(w, values)
	if !ok {
		return
	}
	writeChatAdminResult(w, gateway.BotAPIPromoteChatMember(r.Context(), botID, chatID, userID, adminRightsFromParams(values)))
}

// maxAdminCustomTitleLength 是官方 custom_title 的上限（0-16 个字符）。
const maxAdminCustomTitleLength = 16

func (h *handler) setChatAdministratorCustomTitle(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	userID, ok := chatAdminUserID(w, values)
	if !ok {
		return
	}
	title := values["custom_title"]
	if utf8.RuneCountInString(title) > maxAdminCustomTitleLength {
		writeAPIError(w, http.StatusBadRequest, "ADMIN_RANK_INVALID")
		return
	}
	writeChatAdminResult(w, gateway.BotAPISetChatAdministratorCustomTitle(r.Context(), botID, chatID, userID, title))
}

func (h *handler) setChatPermissions(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	rights, ok := chatPermissionsParam(w, values)
	if !ok {
		return
	}
	writeChatAdminResult(w, gateway.BotAPISetChatPermissions(r.Context(), botID, chatID, rights))
}

func (h *handler) pinChatMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	messageID := apiInt(values["message_id"], 0)
	if messageID <= 0 {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return
	}
	writeChatAdminResult(w, gateway.BotAPIPinChatMessage(r.Context(), botID, chatID, messageID, apiBool(values["disable_notification"])))
}

func (h *handler) unpinChatMessage(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	messageID := apiInt(values["message_id"], 0)
	if messageID < 0 {
		writeAPIError(w, http.StatusBadRequest, "MESSAGE_ID_INVALID")
		return
	}
	writeChatAdminResult(w, gateway.BotAPIUnpinChatMessage(r.Context(), botID, chatID, messageID))
}

func (h *handler) unpinAllChatMessages(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, _, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	writeChatAdminResult(w, gateway.BotAPIUnpinAllChatMessages(r.Context(), botID, chatID))
}

// 官方 setChatTitle 为 1-128 个字符，setChatDescription 为 0-255 个字符。
const (
	maxChatTitleLength       = 128
	maxChatDescriptionLength = 255
)

func (h *handler) setChatTitle(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	title := values["title"]
	switch n := utf8.RuneCountInString(title); {
	case n == 0:
		writeAPIError(w, http.StatusBadRequest, "CHAT_TITLE_EMPTY")
		return
	case n > maxChatTitleLength:
		writeAPIError(w, http.StatusBadRequest, "TITLE_INVALID")
		return
	}
	writeChatAdminResult(w, gateway.BotAPISetChatTitle(r.Context(), botID, chatID, title))
}

func (h *handler) setChatDescription(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	description := values["description"]
	if utf8.RuneCountInString(description) > maxChatDescriptionLength {
		writeAPIError(w, http.StatusBadRequest, "CHAT_ABOUT_TOO_LONG")
		return
	}
	writeChatAdminResult(w, gateway.BotAPISetChatDescription(r.Context(), botID, chatID, description))
}

// setChatPhoto 的 photo 只能是本次请求上传的 InputFile：字段本身就是文件，或用
// attach://<field> 指向另一个文件字段。file_id 与 URL 官方都不接受。
func (h *handler) setChatPhoto(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, files, chatID, ok := h.chatAdminRequestWithFiles(w, r, botID)
	if !ok {
		return
	}
	field := "photo"
	if ref, isAttach := strings.CutPrefix(strings.TrimSpace(values["photo"]), "attach://"); isAttach {
		field = ref
	}
	file, found := files[field]
	if !found || len(file.Bytes) == 0 {
		writeAPIError(w, http.StatusBadRequest, "PHOTO_INVALID")
		return
	}
	writeChatAdminResult(w, gateway.BotAPISetChatPhoto(r.Context(), botID, chatID, file.Name, file.MimeType, file.Bytes))
}

func (h *handler) deleteChatPhoto(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, _, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	writeChatAdminResult(w, gateway.BotAPIDeleteChatPhoto(r.Context(), botID, chatID))
}

func (h *handler) leaveChat(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, _, chatID, ok := h.chatAdminRequest(w, r, botID)
	if !ok {
		return
	}
	writeChatAdminResult(w, gateway.BotAPILeaveChat(r.Context(), botID, chatID))
}
