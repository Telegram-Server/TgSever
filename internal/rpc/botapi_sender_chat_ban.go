package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Bot API 频道身份封禁（#139）。以机器人身份对频道 peer 调用 channels.editBanned，与真人
// 管理员同一路径：权限（ban_users）、封禁记录、幂等与管理日志都在处理器及其下层。

// BotAPIBanChatSenderChat 实现 banChatSenderChat：全封（view_messages）。
func (r *Router) BotAPIBanChatSenderChat(ctx context.Context, botID, chatID, senderChatID int64) error {
	return r.botAPIEditBannedSenderChat(ctx, botID, chatID, senderChatID, tg.ChatBannedRights{ViewMessages: true})
}

// BotAPIUnbanChatSenderChat 实现 unbanChatSenderChat：禁止位全空即解封。
func (r *Router) BotAPIUnbanChatSenderChat(ctx context.Context, botID, chatID, senderChatID int64) error {
	return r.botAPIEditBannedSenderChat(ctx, botID, chatID, senderChatID, tg.ChatBannedRights{})
}

func (r *Router) botAPIEditBannedSenderChat(ctx context.Context, botID, chatID, senderChatID int64, rights tg.ChatBannedRights) error {
	sender, ok := botAPIPeerFromChatID(senderChatID)
	if !ok || sender.Type != domain.PeerTypeChannel {
		return errors.New("SENDER_CHAT_ID_INVALID")
	}
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	// 处理器按频道 id 定位被封频道，不校验 access_hash：bot 通常不是该频道的成员。
	_, err = r.onChannelsEditBanned(WithUserID(ctx, botID), &tg.ChannelsEditBannedRequest{
		Channel:      channel,
		Participant:  &tg.InputPeerChannel{ChannelID: sender.ID},
		BannedRights: rights,
	})
	return err
}
