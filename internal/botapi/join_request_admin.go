package botapi

import (
	"context"
	"net/http"
)

// JoinRequestGateway 是入群申请审批方法的可选网关。生产实现以机器人身份调用
// messages.hideChatJoinRequest，权限校验、成员更新与待审批计数推送都在那里。
type JoinRequestGateway interface {
	BotAPIApproveChatJoinRequest(ctx context.Context, botID, chatID, userID int64) error
	BotAPIDeclineChatJoinRequest(ctx context.Context, botID, chatID, userID int64) error
}

func init() {
	registerMethods(map[string]methodFunc{
		"approveChatJoinRequest": (*handler).approveChatJoinRequest,
		"declineChatJoinRequest": (*handler).declineChatJoinRequest,
	})
	registerErrorMarkers("HIDE_REQUESTER_MISSING", "USER_ALREADY_PARTICIPANT")
}

func (h *handler) approveChatJoinRequest(w http.ResponseWriter, r *http.Request, botID int64) {
	h.answerChatJoinRequest(w, r, botID, JoinRequestGateway.BotAPIApproveChatJoinRequest)
}

func (h *handler) declineChatJoinRequest(w http.ResponseWriter, r *http.Request, botID int64) {
	h.answerChatJoinRequest(w, r, botID, JoinRequestGateway.BotAPIDeclineChatJoinRequest)
}

// answerChatJoinRequest 解析两个方法共有的 chat_id / user_id，调用网关并写回 True 或错误。
func (h *handler) answerChatJoinRequest(w http.ResponseWriter, r *http.Request, botID int64, call func(JoinRequestGateway, context.Context, int64, int64, int64) error) {
	gateway, ok := h.gateway.(JoinRequestGateway)
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
	userID, ok := chatAdminUserID(w, values)
	if !ok {
		return
	}
	if err := call(gateway, r.Context(), botID, chatID, userID); err != nil {
		joinRequestErrors.writeError(w, err)
		return
	}
	writeAPIOK(w, true)
}
