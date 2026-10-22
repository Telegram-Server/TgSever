package rpc

import (
	"context"
	"errors"
	"slices"

	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// BotAPIDeleteMessage 实现 deleteMessage。私聊里消息必须在 chat_id 对应的会话中，
// 按 revoke 删除让对端客户端收到正常的删除 update；群/频道走 channels.deleteMessages
// 的删除主体，自己的消息无需管理员身份，别人的消息需要删除消息权限。找不到消息时
// 报 MESSAGE_ID_INVALID（网关映射为 "message to delete not found"），不返回 true。
func (r *Router) BotAPIDeleteMessage(ctx context.Context, botID, chatID int64, messageID int) (bool, error) {
	if err := r.botAPIDeleteMessages(ctx, botID, chatID, []int{messageID}); err != nil {
		return false, err
	}
	return true, nil
}

// BotAPIDeleteMessages 实现 deleteMessages：一次 1–100 条，与官方一致能删的照删并
// 返回 nil，一条也删不了时才返回错误（错误码与 deleteMessage 相同）。整批只做一次
// 删除，成员只收到一次合并的删除更新。
func (r *Router) BotAPIDeleteMessages(ctx context.Context, botID, chatID int64, messageIDs []int) error {
	if len(messageIDs) == 0 {
		return errors.New("MESSAGE_IDS_EMPTY")
	}
	if len(messageIDs) > domain.MaxDeleteMessageIDs {
		return errors.New("MESSAGE_IDS_TOO_MUCH")
	}
	return r.botAPIDeleteMessages(ctx, botID, chatID, messageIDs)
}

// botAPIDeleteMessages 是 deleteMessage / deleteMessages 共用的删除桥接。
func (r *Router) botAPIDeleteMessages(ctx context.Context, botID, chatID int64, messageIDs []int) error {
	if r == nil || botID == 0 {
		return errors.New("BOT_INVALID")
	}
	// 超出消息盒范围的 id 不可能指向任何消息，按"找不到"跳过。
	ids := make([]int, 0, len(messageIDs))
	for _, id := range messageIDs {
		if id > 0 && id <= domain.MaxMessageBoxID {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return errors.New("MESSAGE_ID_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return errors.New("CHAT_ID_INVALID")
	}
	if peer.Type == domain.PeerTypeChannel {
		return r.botAPIDeleteChannelMessages(ctx, botID, chatID, ids)
	}
	return r.botAPIDeletePrivateMessages(ctx, botID, peer, ids)
}

// botAPIDeletePrivateMessages 先在机器人消息盒里查出消息，只保留属于 peer 会话的
// 那些：消息盒 id 在机器人所有私聊间共用一个序列，不校验时错误的 chat_id 会删掉
// 另一个用户会话里的同号消息。
func (r *Router) botAPIDeletePrivateMessages(ctx context.Context, botID int64, peer domain.Peer, ids []int) error {
	if r.deps.Messages == nil {
		return errors.New("BOT_INVALID")
	}
	list, err := r.deps.Messages.GetMessages(ctx, botID, ids)
	if err != nil {
		return err
	}
	inChat := make([]int, 0, len(ids))
	for _, msg := range list.Messages {
		if msg.Peer == peer && !msg.Deleted && slices.Contains(ids, msg.ID) && !slices.Contains(inChat, msg.ID) {
			inChat = append(inChat, msg.ID)
		}
	}
	if len(inChat) == 0 {
		return errors.New("MESSAGE_ID_INVALID")
	}
	res, err := r.deps.Messages.DeleteMessages(ctx, botID, domain.DeleteMessagesRequest{
		OwnerUserID: botID,
		IDs:         inChat,
		Revoke:      true,
		Date:        int(r.clock.Now().Unix()),
		Audit:       domain.BotDeletionAudit(botID),
	})
	if err != nil {
		return err
	}
	r.notifyPushDeletes(res)
	if len(res.Self().MessageIDs) == 0 {
		// 查到之后被并发删除。
		return errors.New("MESSAGE_ID_INVALID")
	}
	return nil
}

// botAPIDeleteChannelMessages 以机器人身份删除群/频道消息，权限校验与扇出和
// channels.deleteMessages 相同，删除记录归属于机器人。
//
// 存储层的批量删除只要有一条"别人的消息且无删除权"就整批失败，而官方语义是能删
// 的照删。没有删除权的机器人只能删自己的消息，所以先把这批 id 收窄到机器人自己发
// 的消息，再做一次删除：整批仍是一次删除、一次删除更新，不逐条删（逐条删会给每个
// 成员每条消息各推一次更新）。
func (r *Router) botAPIDeleteChannelMessages(ctx context.Context, botID, chatID int64, ids []int) error {
	channel, view, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	targets := ids
	if !botAPICanDeleteAnyChannelMessage(view.Self) {
		own, err := r.botAPIOwnChannelMessageIDs(ctx, botID, channel.ChannelID, ids)
		if err != nil {
			return botAPIChannelDeleteErr(err)
		}
		// 一条自己的也没有时仍按原 id 交给存储层，由它给出准确的失败原因
		// （别人的消息 → MESSAGE_DELETE_FORBIDDEN，全不存在 → 下面的 MESSAGE_ID_INVALID）。
		if len(own) > 0 {
			targets = own
		}
	}
	res, err := r.deleteChannelMessages(WithUserID(ctx, botID), botID, channel.ChannelID, targets, domain.BotDeletionAudit(botID))
	if err != nil {
		return botAPIChannelDeleteErr(err)
	}
	if len(res.DeletedIDs) == 0 {
		if slices.Contains(ids, 1) {
			// id=1 建群服务消息存在但存储层恒跳过，属于不能删除而不是找不到。
			return tgerr400("MESSAGE_DELETE_FORBIDDEN")
		}
		return errors.New("MESSAGE_ID_INVALID")
	}
	return nil
}

// botAPICanDeleteAnyChannelMessage 与存储层的删除权判定一致：群主，或有删除消息
// 权限的管理员。
func botAPICanDeleteAnyChannelMessage(self domain.ChannelMember) bool {
	return self.Role == domain.ChannelRoleCreator ||
		(self.Role == domain.ChannelRoleAdmin && self.AdminRights.DeleteMessages)
}

// botAPIOwnChannelMessageIDs 返回 ids 中机器人自己发的、尚未删除的消息（升序）。
func (r *Router) botAPIOwnChannelMessageIDs(ctx context.Context, botID, channelID int64, ids []int) ([]int, error) {
	history, err := r.deps.Channels.GetMessages(ctx, botID, channelID, ids)
	if err != nil {
		return nil, err
	}
	own := make([]int, 0, len(ids))
	for _, msg := range history.Messages {
		if msg.SenderUserID == botID && !msg.Deleted && slices.Contains(ids, msg.ID) && !slices.Contains(own, msg.ID) {
			own = append(own, msg.ID)
		}
	}
	slices.Sort(own)
	return own, nil
}

// botAPIChannelDeleteErr 与其他 Bot API 群方法一致：机器人已不在群里/被封禁时按
// chat_id 无效处理。
func botAPIChannelDeleteErr(err error) error {
	if tgerr.Is(err, "CHANNEL_PRIVATE", "CHANNEL_INVALID", "USER_BANNED_IN_CHANNEL") {
		return errors.New("CHAT_ID_INVALID")
	}
	return err
}
