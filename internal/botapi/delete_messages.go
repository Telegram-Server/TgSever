package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
)

// DeleteMessagesGateway 是 deleteMessages 的可选网关面。
type DeleteMessagesGateway interface {
	// BotAPIDeleteMessages 删除 chat_id 会话里的一批消息：能删的照删，返回 nil；
	// 一条也删不了时返回错误（MESSAGE_ID_INVALID / MESSAGE_DELETE_FORBIDDEN 等）。
	BotAPIDeleteMessages(ctx context.Context, botID, chatID int64, messageIDs []int) error
}

// maxDeleteMessageIDs 是 deleteMessages 一次最多处理的消息数（官方上限 100）。
const maxDeleteMessageIDs = 100

func init() {
	registerMethods(map[string]methodFunc{
		"deleteMessages": (*handler).deleteMessages,
	})
}

func (h *handler) deleteMessages(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(DeleteMessagesGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, err := h.chatIDParam(r.Context(), botID, values["chat_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	ids, err := deleteMessageIDs(values["message_ids"])
	if err != nil {
		deleteMessagesErrors.writeError(w, err)
		return
	}
	if err := gateway.BotAPIDeleteMessages(r.Context(), botID, chatID, ids); err != nil {
		deleteMessagesErrors.writeError(w, err)
		return
	}
	writeAPIOK(w, true)
}

// deleteMessageIDs 解析 message_ids：1–100 个正整数（JSON 数组），去重后升序。
// 与 forwardMessageIDs 不同，超限和非法 id 各有自己的错误码：deleteMessages 的
// MESSAGE_ID_INVALID 表示"要删的消息不存在"，不能拿来报参数错误。
func deleteMessageIDs(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("MESSAGE_IDS_EMPTY")
	}
	var ids []int
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, errors.New("MESSAGE_IDS_INVALID")
	}
	if len(ids) == 0 {
		return nil, errors.New("MESSAGE_IDS_EMPTY")
	}
	if len(ids) > maxDeleteMessageIDs {
		return nil, errors.New("MESSAGE_IDS_TOO_MUCH")
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, errors.New("MESSAGE_IDS_INVALID")
		}
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}
