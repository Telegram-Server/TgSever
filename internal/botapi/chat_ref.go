package botapi

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

// ChatRefGateway 把公开 @username 解析成 Bot API chat_id（用户为正数，超级群/
// 频道为 -100 前缀）。可选接口：网关不实现时只接受数字 chat_id。
type ChatRefGateway interface {
	BotAPIResolveChatUsername(ctx context.Context, botID int64, username string) (int64, error)
}

func init() {
	registerErrorMarkers("USERNAME_NOT_OCCUPIED", "USERNAME_INVALID")
}

// chatIDParam 解析官方 chat_id 参数：Integer or String，字符串形如 @channelusername。
func (h *handler) chatIDParam(ctx context.Context, botID int64, raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if username, ok := strings.CutPrefix(raw, "@"); ok {
		if !validChatUsername(username) {
			return 0, errors.New("CHAT_ID_INVALID")
		}
		resolver, ok := h.gateway.(ChatRefGateway)
		if !ok {
			return 0, errors.New("CHAT_ID_INVALID")
		}
		return resolver.BotAPIResolveChatUsername(ctx, botID, username)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("CHAT_ID_INVALID")
	}
	return id, nil
}

func validChatUsername(username string) bool {
	if username == "" {
		return false
	}
	for _, r := range username {
		if !(r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}
