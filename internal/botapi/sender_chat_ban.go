package botapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// SenderChatBanGateway 是频道身份封禁（banChatSenderChat / unbanChatSenderChat）的可选网关。
// 生产实现以机器人身份对频道 peer 调用 channels.editBanned，与真人管理员同一路径：
// 权限校验、封禁记录与管理日志都在那里。senderChatID 是 Bot API 形式（-100 前缀）。
type SenderChatBanGateway interface {
	BotAPIBanChatSenderChat(ctx context.Context, botID, chatID, senderChatID int64) error
	BotAPIUnbanChatSenderChat(ctx context.Context, botID, chatID, senderChatID int64) error
}

// errSenderChatIDInvalid 是网关自己的参数错误：sender_chat_id 缺失、无法解析或不是
// 超级群/频道的 chat_id。
var errSenderChatIDInvalid = errors.New("SENDER_CHAT_ID_INVALID")

// senderChatBanErrors 是两个方法的官方失败说明。官方网关经 TDLib 的
// setChatMemberStatus 执行：没有封禁权限报 restrict/unrestrict 那句；sender_chat_id
// 不是合法的 chat 标识报 invalid chat identifier；目标频道不存在（处理器的
// PEER_ID_INVALID）报 member not found。
var senderChatBanErrors = newOfficialErrorDescriptions(map[string]string{
	"CHAT_ADMIN_REQUIRED":    "Bad Request: not enough rights to restrict/unrestrict chat member",
	"SENDER_CHAT_ID_INVALID": "Bad Request: invalid chat identifier specified",
	"PEER_ID_INVALID":        "Bad Request: member not found",
})

func init() {
	registerMethods(map[string]methodFunc{
		"banChatSenderChat":   (*handler).banChatSenderChat,
		"unbanChatSenderChat": (*handler).unbanChatSenderChat,
	})
}

func (h *handler) banChatSenderChat(w http.ResponseWriter, r *http.Request, botID int64) {
	h.editSenderChatBan(w, r, botID, SenderChatBanGateway.BotAPIBanChatSenderChat)
}

func (h *handler) unbanChatSenderChat(w http.ResponseWriter, r *http.Request, botID int64) {
	h.editSenderChatBan(w, r, botID, SenderChatBanGateway.BotAPIUnbanChatSenderChat)
}

// editSenderChatBan 解析两个方法共有的 chat_id / sender_chat_id，调用网关并写回 True 或错误。
func (h *handler) editSenderChatBan(w http.ResponseWriter, r *http.Request, botID int64, call func(SenderChatBanGateway, context.Context, int64, int64, int64) error) {
	gateway, ok := h.gateway.(SenderChatBanGateway)
	if !ok || h.gateway == nil {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	chatID, err := h.chatIDParam(r.Context(), botID, values["chat_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	senderChatID, err := senderChatIDParam(values["sender_chat_id"])
	if err != nil {
		senderChatBanErrors.writeError(w, err)
		return
	}
	if err := call(gateway, r.Context(), botID, chatID, senderChatID); err != nil {
		senderChatBanErrors.writeError(w, err)
		return
	}
	writeAPIOK(w, true)
}

// senderChatIDParam 解析 sender_chat_id：只接受超级群/频道的数字 chat_id（-100 前缀）。
// 本服没有旧式普通群，用户不能"以频道身份"发言，其余一律是参数错误。
func senderChatIDParam(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id >= botAPIChannelChatIDMin {
		return 0, errSenderChatIDInvalid
	}
	return id, nil
}
