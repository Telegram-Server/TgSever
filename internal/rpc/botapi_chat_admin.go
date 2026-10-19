package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Bot API 群管理方法。全部以机器人身份调用 MTProto 处理器：权限校验、服务消息、
// 缓存失效、在线成员维护与更新扇出都在处理器里，见 botapi_bridge.go。

// botAPIFullBan 是 banChatMember 的禁止位：连同 view_messages 在内全部禁止。
func botAPIFullBan(untilDate int) domain.ChannelBannedRights {
	return domain.ChannelBannedRights{
		ViewMessages: true, SendMessages: true, SendMedia: true, SendStickers: true,
		SendGifs: true, SendGames: true, SendInline: true, EmbedLinks: true,
		SendPolls: true, ChangeInfo: true, InviteUsers: true, PinMessages: true,
		ManageTopics: true, SendPhotos: true, SendVideos: true, SendRoundvideos: true,
		SendAudios: true, SendVoices: true, SendDocs: true, SendPlain: true,
		SendReactions: true, UntilDate: untilDate,
	}
}

// botAPIEditBanned 以机器人身份对群成员调用 channels.editBanned。
func (r *Router) botAPIEditBanned(ctx context.Context, botID, chatID, userID int64, rights domain.ChannelBannedRights) error {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	user, err := r.botAPIInputUser(ctx, botID, userID)
	if err != nil {
		return err
	}
	_, err = r.onChannelsEditBanned(WithUserID(ctx, botID), &tg.ChannelsEditBannedRequest{
		Channel:      channel,
		Participant:  &tg.InputPeerUser{UserID: user.UserID, AccessHash: user.AccessHash},
		BannedRights: tgChatBannedRights(rights),
	})
	return err
}

// BotAPIBanChatMember 实现 banChatMember。
// revoke_messages 时再用 channels.deleteParticipantHistory 分批删除该用户的全部消息。
func (r *Router) BotAPIBanChatMember(ctx context.Context, botID, chatID, userID int64, untilDate int, revokeMessages bool) error {
	if err := r.botAPIEditBanned(ctx, botID, chatID, userID, botAPIFullBan(untilDate)); err != nil {
		return err
	}
	if !revokeMessages {
		return nil
	}
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	user, err := r.botAPIInputUser(ctx, botID, userID)
	if err != nil {
		return err
	}
	req := &tg.ChannelsDeleteParticipantHistoryRequest{
		Channel:     channel,
		Participant: &tg.InputPeerUser{UserID: user.UserID, AccessHash: user.AccessHash},
	}
	// offset 非 0 表示还有下一批；上限只防处理器异常时死循环。
	for range botAPIMaxRevokeBatches {
		affected, err := r.onChannelsDeleteParticipantHistory(WithUserID(ctx, botID), req)
		if err != nil {
			return err
		}
		if affected.Offset == 0 {
			return nil
		}
	}
	return nil
}

const botAPIMaxRevokeBatches = 10000

// BotAPIRestrictChatMember 实现 restrictChatMember；rights 已含 until_date。
func (r *Router) BotAPIRestrictChatMember(ctx context.Context, botID, chatID, userID int64, rights domain.ChannelBannedRights) error {
	return r.botAPIEditBanned(ctx, botID, chatID, userID, rights)
}

// botAPIEditAdmin 以机器人身份调用 channels.editAdmin。rank 为 nil 时保留原头衔。
func (r *Router) botAPIEditAdmin(ctx context.Context, botID, chatID, userID int64, rights domain.ChannelAdminRights, rank *string) error {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	user, err := r.botAPIInputUser(ctx, botID, userID)
	if err != nil {
		return err
	}
	// tgChatAdminRights 恒置 other（manage_chat）；官方语义里它由任一管理权限隐含，
	// 全部为 false 时必须清掉，否则撤销管理员会留下一个只有 manage_chat 的管理员。
	adminRights := tgChatAdminRights(rights)
	adminRights.Other = rights != (domain.ChannelAdminRights{})
	req := &tg.ChannelsEditAdminRequest{Channel: channel, UserID: user, AdminRights: adminRights}
	if rank != nil {
		req.SetRank(*rank)
	}
	_, err = r.onChannelsEditAdmin(WithUserID(ctx, botID), req)
	return err
}

// BotAPIPromoteChatMember 实现 promoteChatMember；权限全为 false 即撤销管理员。
func (r *Router) BotAPIPromoteChatMember(ctx context.Context, botID, chatID, userID int64, rights domain.ChannelAdminRights) error {
	return r.botAPIEditAdmin(ctx, botID, chatID, userID, rights, nil)
}

// BotAPISetChatAdministratorCustomTitle 只改头衔：以目标当前的管理权限重发 editAdmin。
// 官方只允许给机器人提升的管理员设置头衔，非管理员一律拒绝。
func (r *Router) BotAPISetChatAdministratorCustomTitle(ctx context.Context, botID, chatID, userID int64, title string) error {
	_, view, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	member, err := r.deps.Channels.GetParticipant(ctx, botID, view.Channel.ID, userID)
	if err != nil {
		return channelParticipantErr(err)
	}
	if member.Status != domain.ChannelMemberActive || member.Role != domain.ChannelRoleAdmin {
		return errors.New("USER_NOT_ADMIN")
	}
	return r.botAPIEditAdmin(ctx, botID, chatID, userID, member.AdminRights, &title)
}

// botAPIInputChannelPeer 是超级群/频道 chat_id 的 InputPeerChannel。
func (r *Router) botAPIInputChannelPeer(ctx context.Context, botID, chatID int64) (*tg.InputPeerChannel, error) {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return nil, err
	}
	return &tg.InputPeerChannel{ChannelID: channel.ChannelID, AccessHash: channel.AccessHash}, nil
}

// BotAPISetChatPermissions 实现 setChatPermissions（messages.editChatDefaultBannedRights）。
func (r *Router) BotAPISetChatPermissions(ctx context.Context, botID, chatID int64, rights domain.ChannelBannedRights) error {
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return err
	}
	_, err = r.onMessagesEditChatDefaultBannedRights(WithUserID(ctx, botID), &tg.MessagesEditChatDefaultBannedRightsRequest{
		Peer:         peer,
		BannedRights: tgChatBannedRights(rights),
	})
	return err
}

// BotAPIPinChatMessage 实现 pinChatMessage；私聊与群都走 messages.updatePinnedMessage。
func (r *Router) BotAPIPinChatMessage(ctx context.Context, botID, chatID int64, messageID int, silent bool) error {
	return r.botAPIUpdatePinned(ctx, botID, chatID, messageID, false, silent)
}

// BotAPIUnpinChatMessage 实现 unpinChatMessage；messageID 为 0 时取消最近一条置顶。
func (r *Router) BotAPIUnpinChatMessage(ctx context.Context, botID, chatID int64, messageID int) error {
	if messageID == 0 {
		latest, err := r.botAPILatestPinnedMessageID(ctx, botID, chatID)
		if err != nil {
			return err
		}
		if latest == 0 {
			return errors.New("MESSAGE_ID_INVALID")
		}
		messageID = latest
	}
	return r.botAPIUpdatePinned(ctx, botID, chatID, messageID, true, false)
}

func (r *Router) botAPIUpdatePinned(ctx context.Context, botID, chatID int64, messageID int, unpin, silent bool) error {
	peer, _, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return err
	}
	_, err = r.onMessagesUpdatePinnedMessage(WithUserID(ctx, botID), &tg.MessagesUpdatePinnedMessageRequest{
		Peer:   peer,
		ID:     messageID,
		Unpin:  unpin,
		Silent: silent,
	})
	return err
}

// botAPILatestPinnedMessageID 返回机器人视角下会话当前最顶的置顶消息 id，没有时为 0。
func (r *Router) botAPILatestPinnedMessageID(ctx context.Context, botID, chatID int64) (int, error) {
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return 0, errors.New("CHAT_ID_INVALID")
	}
	if peer.Type == domain.PeerTypeChannel {
		_, view, err := r.botAPIInputChannel(ctx, botID, chatID)
		if err != nil {
			return 0, err
		}
		return view.Channel.PinnedMessageID, nil
	}
	if r.deps.Messages == nil {
		return 0, errors.New("BOT_INVALID")
	}
	if _, err := r.botAPIInputUser(ctx, botID, peer.ID); err != nil {
		return 0, errors.New("CHAT_ID_INVALID")
	}
	list, err := r.deps.Messages.Search(ctx, botID, domain.MessageFilter{
		HasPeer: true, Peer: peer, PinnedOnly: true, Limit: 1,
	})
	if err != nil {
		return 0, err
	}
	if len(list.Messages) == 0 {
		return 0, nil
	}
	return list.Messages[0].ID, nil
}

// BotAPIUnpinAllChatMessages 实现 unpinAllChatMessages。
func (r *Router) BotAPIUnpinAllChatMessages(ctx context.Context, botID, chatID int64) error {
	peer, _, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return err
	}
	_, err = r.onMessagesUnpinAllMessages(WithUserID(ctx, botID), &tg.MessagesUnpinAllMessagesRequest{Peer: peer})
	return err
}

// BotAPISetChatTitle 实现 setChatTitle（channels.editTitle）。
func (r *Router) BotAPISetChatTitle(ctx context.Context, botID, chatID int64, title string) error {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	_, err = r.onChannelsEditTitle(WithUserID(ctx, botID), &tg.ChannelsEditTitleRequest{Channel: channel, Title: title})
	return err
}

// BotAPISetChatDescription 实现 setChatDescription（messages.editChatAbout）。
func (r *Router) BotAPISetChatDescription(ctx context.Context, botID, chatID int64, description string) error {
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return err
	}
	_, err = r.onMessagesEditChatAbout(WithUserID(ctx, botID), &tg.MessagesEditChatAboutRequest{Peer: peer, About: description})
	return err
}

// BotAPISetChatPhoto 实现 setChatPhoto：上传的图片先经 botAPIMedia 落成 photo，
// 再以 InputChatPhoto 引用它调用 channels.editPhoto。
func (r *Router) BotAPISetChatPhoto(ctx context.Context, botID, chatID int64, fileName, mimeType string, data []byte) error {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	if r.deps.Files == nil || len(data) == 0 {
		return photoInvalidErr()
	}
	media, err := r.botAPIMedia(ctx, botID, "photo", "", "", fileName, mimeType, data)
	if err != nil {
		return err
	}
	if media.Photo == nil {
		return photoInvalidErr()
	}
	photo := media.Photo
	return r.botAPIEditPhoto(ctx, botID, channel, &tg.InputChatPhoto{
		ID: &tg.InputPhoto{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference},
	})
}

// BotAPIDeleteChatPhoto 实现 deleteChatPhoto。
func (r *Router) BotAPIDeleteChatPhoto(ctx context.Context, botID, chatID int64) error {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	return r.botAPIEditPhoto(ctx, botID, channel, &tg.InputChatPhotoEmpty{})
}

func (r *Router) botAPIEditPhoto(ctx context.Context, botID int64, channel *tg.InputChannel, photo tg.InputChatPhotoClass) error {
	_, err := r.onChannelsEditPhoto(WithUserID(ctx, botID), &tg.ChannelsEditPhotoRequest{Channel: channel, Photo: photo})
	return err
}

// BotAPILeaveChat 实现 leaveChat（channels.leaveChannel）；私聊没有可退出的会话。
func (r *Router) BotAPILeaveChat(ctx context.Context, botID, chatID int64) error {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	_, err = r.onChannelsLeaveChannel(WithUserID(ctx, botID), channel)
	return err
}

// BotAPIUnbanChatMember 实现 unbanChatMember 的官方语义：调用后用户不在群里但可以
// 再加入。正常成员先踢出再解封；only_if_banned 时只处理被踢出的用户。
func (r *Router) BotAPIUnbanChatMember(ctx context.Context, botID, chatID, userID int64, onlyIfBanned bool) error {
	_, view, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	status := domain.ChannelMemberLeft
	member, err := r.deps.Channels.GetParticipant(ctx, botID, view.Channel.ID, userID)
	switch {
	case err == nil:
		status = member.Status
	case !errors.Is(err, domain.ErrUserNotParticipant):
		return channelParticipantErr(err)
	}
	if onlyIfBanned && status != domain.ChannelMemberKicked {
		return nil
	}
	if status == domain.ChannelMemberActive {
		if err := r.botAPIEditBanned(ctx, botID, chatID, userID, botAPIFullBan(0)); err != nil {
			return err
		}
	}
	return r.botAPIEditBanned(ctx, botID, chatID, userID, domain.ChannelBannedRights{})
}
