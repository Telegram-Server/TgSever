package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
)

// Bot API 入群申请审批：以机器人身份调用 messages.hideChatJoinRequest，成员更新、
// 待审批计数推送与 chat_member 入队都由处理器完成，见 botapi_bridge.go。

// BotAPIApproveChatJoinRequest 实现 approveChatJoinRequest。
func (r *Router) BotAPIApproveChatJoinRequest(ctx context.Context, botID, chatID, userID int64) error {
	return r.botAPIHideChatJoinRequest(ctx, botID, chatID, userID, true)
}

// BotAPIDeclineChatJoinRequest 实现 declineChatJoinRequest。
func (r *Router) BotAPIDeclineChatJoinRequest(ctx context.Context, botID, chatID, userID int64) error {
	return r.botAPIHideChatJoinRequest(ctx, botID, chatID, userID, false)
}

func (r *Router) botAPIHideChatJoinRequest(ctx context.Context, botID, chatID, userID int64, approved bool) error {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	user, err := r.botAPIInputUser(ctx, botID, userID)
	if err != nil {
		return err
	}
	_, err = r.onMessagesHideChatJoinRequest(WithUserID(ctx, botID), &tg.MessagesHideChatJoinRequestRequest{
		Approved: approved,
		Peer:     &tg.InputPeerChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash},
		UserID:   user,
	})
	return err
}
