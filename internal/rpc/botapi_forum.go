package rpc

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) BotAPIGetForumTopicIconStickers(ctx context.Context, botID int64) ([]domain.Document, error) {
	if r.deps.Files == nil {
		return nil, internalErr()
	}
	_, docs, found, err := r.deps.Files.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefBySystem, SystemKey: domain.StickerSetSystemKeyEmojiDefaultTopicIcons})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, stickersetInvalidErr()
	}
	out := make([]domain.Document, 0, len(docs))
	for _, doc := range docs {
		for _, attr := range doc.Attributes {
			if attr.Kind == domain.DocAttrCustomEmoji {
				out = append(out, doc)
				break
			}
		}
	}
	return out, nil
}

func (r *Router) botAPIForumIcon(ctx context.Context, botID, emojiID int64) error {
	if emojiID == 0 {
		return nil
	}
	if emojiID < 0 {
		return errors.New("CUSTOM_EMOJI_ID_INVALID")
	}
	docs, err := r.BotAPIGetForumTopicIconStickers(ctx, botID)
	if err != nil {
		return err
	}
	for _, doc := range docs {
		if doc.ID == emojiID {
			return nil
		}
	}
	return errors.New("CUSTOM_EMOJI_ID_INVALID")
}

func (r *Router) botAPIForumChannel(ctx context.Context, botID, chatID int64) (*tg.InputPeerChannel, domain.ChannelView, error) {
	input, view, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return nil, view, err
	}
	if !view.Channel.Forum || !view.Channel.Megagroup || view.Channel.Broadcast {
		return nil, view, channelForumMissingErr()
	}
	if view.Self.Status != domain.ChannelMemberActive {
		return nil, view, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	return &tg.InputPeerChannel{ChannelID: input.ChannelID, AccessHash: input.AccessHash}, view, nil
}

func forumAdmin(member domain.ChannelMember, right bool) bool {
	return member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin && right
}

func (r *Router) BotAPICreateForumTopic(ctx context.Context, botID, chatID int64, name string, iconColor int, iconEmojiID int64) (domain.ChannelForumTopic, error) {
	if err := validateForumTopicTitle(name, false); err != nil {
		return domain.ChannelForumTopic{}, err
	}
	switch iconColor {
	case 0:
		iconColor = domain.DefaultForumTopicIconColor
	case 7322096, 16766590, 13338331, 9367192, 16749490, 16478047:
	default:
		return domain.ChannelForumTopic{}, errors.New("ICON_COLOR_INVALID")
	}
	if err := r.botAPIForumIcon(ctx, botID, iconEmojiID); err != nil {
		return domain.ChannelForumTopic{}, err
	}
	if chatID > 0 {
		return r.botAPICreatePrivateForumTopic(ctx, botID, chatID, name, iconColor, iconEmojiID)
	}
	peer, view, err := r.botAPIForumChannel(ctx, botID, chatID)
	if err != nil {
		return domain.ChannelForumTopic{}, err
	}
	if !forumAdmin(view.Self, view.Self.AdminRights.ManageTopics) {
		return domain.ChannelForumTopic{}, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	req := &tg.MessagesCreateForumTopicRequest{Peer: peer, Title: name, RandomID: randomNonZeroInt64()}
	req.SetIconColor(iconColor)
	if iconEmojiID != 0 {
		req.SetIconEmojiID(iconEmojiID)
	}
	updates, err := r.onMessagesCreateForumTopic(WithUserID(ctx, botID), req)
	if err != nil {
		return domain.ChannelForumTopic{}, err
	}
	ids := botAPIUpdateMessageIDs(domain.Peer{Type: domain.PeerTypeChannel, ID: view.Channel.ID}, updates)
	topics, err := r.deps.Channels.GetForumTopicsByID(ctx, botID, view.Channel.ID, ids)
	if err != nil {
		return domain.ChannelForumTopic{}, forumTopicError(err)
	}
	if len(topics.Topics) != 1 {
		return domain.ChannelForumTopic{}, internalErr()
	}
	return topics.Topics[0], nil
}

func (r *Router) BotAPIEditForumTopic(ctx context.Context, botID, chatID int64, topicID int, name *string, iconEmojiID *int64) error {
	if name != nil {
		if utf8.RuneCountInString(*name) > 128 {
			return topicTitleEmptyErr()
		}
		if *name == "" {
			name = nil
		}
	}
	if iconEmojiID != nil {
		if err := r.botAPIForumIcon(ctx, botID, *iconEmojiID); err != nil {
			return err
		}
	}
	if chatID > 0 {
		return r.botAPIEditPrivateForumTopic(ctx, botID, chatID, topicID, name, iconEmojiID)
	}
	if topicID <= domain.ForumGeneralTopicID {
		return topicIDInvalidErr()
	}
	peer, view, err := r.botAPIForumChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	if err := r.botAPIManageForumTopic(ctx, botID, view, topicID); err != nil {
		return err
	}
	req := &tg.MessagesEditForumTopicRequest{Peer: peer, TopicID: topicID}
	if name != nil {
		req.SetTitle(*name)
	}
	if iconEmojiID != nil {
		req.SetIconEmojiID(*iconEmojiID)
	}
	_, err = r.onMessagesEditForumTopic(WithUserID(ctx, botID), req)
	return err
}

func (r *Router) botAPIManageForumTopic(ctx context.Context, botID int64, view domain.ChannelView, topicID int) error {
	topics, err := r.deps.Channels.GetForumTopicsByID(ctx, botID, view.Channel.ID, []int{topicID})
	if err != nil {
		return forumTopicError(err)
	}
	if len(topics.Topics) != 1 {
		return topicIDInvalidErr()
	}
	if !forumAdmin(view.Self, view.Self.AdminRights.ManageTopics) && topics.Topics[0].CreatorUserID != botID {
		return tgerr400("CHAT_ADMIN_REQUIRED")
	}
	return nil
}

func (r *Router) BotAPISetForumTopicClosed(ctx context.Context, botID, chatID int64, topicID int, closed bool) error {
	if topicID <= domain.ForumGeneralTopicID {
		return topicIDInvalidErr()
	}
	peer, view, err := r.botAPIForumChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	if err := r.botAPIManageForumTopic(ctx, botID, view, topicID); err != nil {
		return err
	}
	req := &tg.MessagesEditForumTopicRequest{Peer: peer, TopicID: topicID}
	req.SetClosed(closed)
	_, err = r.onMessagesEditForumTopic(WithUserID(ctx, botID), req)
	return err
}

func (r *Router) BotAPIEditGeneralForumTopic(ctx context.Context, botID, chatID int64, name *string, closed, hidden *bool) error {
	peer, view, err := r.botAPIForumChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	if !forumAdmin(view.Self, view.Self.AdminRights.ManageTopics) {
		return tgerr400("CHAT_ADMIN_REQUIRED")
	}
	req := &tg.MessagesEditForumTopicRequest{Peer: peer, TopicID: domain.ForumGeneralTopicID}
	if name != nil {
		if strings.TrimSpace(*name) == "" {
			return topicTitleEmptyErr()
		}
		req.SetTitle(*name)
	}
	if closed != nil {
		req.SetClosed(*closed)
	}
	if hidden != nil {
		req.SetHidden(*hidden)
	}
	_, err = r.onMessagesEditForumTopic(WithUserID(ctx, botID), req)
	return err
}

func (r *Router) BotAPIDeleteForumTopic(ctx context.Context, botID, chatID int64, topicID int) error {
	if chatID > 0 {
		return r.botAPIDeletePrivateForumTopic(ctx, botID, chatID, topicID)
	}
	if topicID <= domain.ForumGeneralTopicID {
		return topicIDInvalidErr()
	}
	peer, view, err := r.botAPIForumChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	if !forumAdmin(view.Self, view.Self.AdminRights.DeleteMessages) {
		return tgerr400("CHAT_ADMIN_REQUIRED")
	}
	for {
		affected, err := r.onMessagesDeleteTopicHistory(WithUserID(ctx, botID), &tg.MessagesDeleteTopicHistoryRequest{Peer: peer, TopMsgID: topicID})
		if err != nil {
			return err
		}
		if affected.Offset == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (r *Router) BotAPIUnpinAllForumTopicMessages(ctx context.Context, botID, chatID int64, topicID int) error {
	if chatID > 0 {
		return r.botAPIUnpinAllPrivateForumTopicMessages(ctx, botID, chatID, topicID)
	}
	if topicID <= 0 {
		return topicIDInvalidErr()
	}
	peer, view, err := r.botAPIForumChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	if !forumAdmin(view.Self, view.Self.AdminRights.PinMessages) {
		return tgerr400("CHAT_ADMIN_REQUIRED")
	}
	req := &tg.MessagesUnpinAllMessagesRequest{Peer: peer}
	req.SetTopMsgID(topicID)
	for {
		affected, err := r.onMessagesUnpinAllMessages(WithUserID(ctx, botID), req)
		if err != nil {
			return err
		}
		if affected.Offset == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
