package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Bot API 的 editMessageCaption / editMessageReplyMarkup / editMessageMedia / stopPoll。
// chat_id+message_id 形式尽量以机器人身份调用 messages.editMessage；inline_message_id
// 形式调用 messages.editInlineBotMessage（与 BotAPIEditInlineMessageText 同一路径）。

// botAPIEditReplyMarkup 把"是否设置键盘"转成 TL：设置但为空即清空键盘。
func botAPIEditReplyMarkup(setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) tg.ReplyMarkupClass {
	if !setReplyMarkup {
		return nil
	}
	if wire := tgReplyMarkup(replyMarkup); wire != nil {
		return wire
	}
	return &tg.ReplyInlineMarkup{}
}

// botAPIEditViaHandler 以机器人身份调用 messages.editMessage 并取回编辑后的消息。
func (r *Router) botAPIEditViaHandler(ctx context.Context, botID, chatID int64, req *tg.MessagesEditMessageRequest) (domain.Message, error) {
	if r == nil || botID == 0 {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return domain.Message{}, errors.New("MESSAGE_ID_INVALID")
	}
	input, peer, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return domain.Message{}, err
	}
	req.Peer = input
	updates, err := r.onMessagesEditMessage(WithUserID(ctx, botID), req)
	if err != nil {
		return domain.Message{}, err
	}
	msgs, err := r.botAPIMessagesFromUpdates(ctx, botID, peer, updates)
	if err != nil {
		return domain.Message{}, err
	}
	if len(msgs) == 0 {
		return r.botAPIReloadMessage(ctx, botID, peer, req.ID)
	}
	return msgs[0], nil
}

// BotAPIEditMessageCaption 实现 editMessageCaption（chat_id+message_id）。
func (r *Router) BotAPIEditMessageCaption(ctx context.Context, botID, chatID int64, messageID int, caption string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	req := &tg.MessagesEditMessageRequest{ID: messageID}
	req.SetMessage(caption)
	if len(entities) > 0 {
		req.SetEntities(tgMessageEntities(entities))
	}
	if wire := botAPIEditReplyMarkup(setReplyMarkup, replyMarkup); wire != nil {
		req.SetReplyMarkup(wire)
	}
	return r.botAPIEditViaHandler(ctx, botID, chatID, req)
}

// botAPIStoredEdit 是绕过 messages.editMessage 的编辑：该处理器只接受带文本的编辑、
// 也不接受替换照片/文档，而 editMessageReplyMarkup（只换键盘）与 editMessageMedia
// 都需要。做法与 BotAPIEditRichMessage 相同：直接走 store 的编辑状态机（作者与权限
// 校验在 store 内），再补上处理器会做的扇出。
type botAPIStoredEdit struct {
	media          *domain.MessageMedia // 非 nil 时替换媒体，caption 同时替换正文
	caption        string
	entities       []domain.MessageEntity
	setReplyMarkup bool
	replyMarkup    *domain.MessageReplyMarkup
}

func (r *Router) botAPIEditStored(ctx context.Context, botID, chatID int64, messageID int, edit botAPIStoredEdit) (domain.Message, error) {
	if r == nil || botID == 0 {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	if messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return domain.Message{}, errors.New("MESSAGE_ID_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	if edit.setReplyMarkup {
		if err := domain.ValidateReplyMarkup(edit.replyMarkup); err != nil {
			return domain.Message{}, replyMarkupErr(err)
		}
		if err := r.validateReplyMarkupForPeer(ctx, botID, peer, edit.replyMarkup); err != nil {
			return domain.Message{}, err
		}
	}
	current, err := r.botAPIReloadMessage(ctx, botID, peer, messageID)
	if err != nil {
		return domain.Message{}, err
	}
	text, entities := current.Body, current.Entities
	if edit.media != nil {
		if err := botAPIValidateMediaReplacement(current, edit.media); err != nil {
			return domain.Message{}, err
		}
		text, entities = edit.caption, r.augmentBotAPIAutoEntities(edit.caption, edit.entities)
	}
	if edit.setReplyMarkup {
		media := current.Media
		if edit.media != nil {
			media = edit.media
		}
		if err := domain.ValidateReplyMarkupMedia(edit.replyMarkup, media); err != nil {
			return domain.Message{}, replyMarkupErr(err)
		}
	}
	now := int(r.clock.Now().Unix())
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return domain.Message{}, errors.New("CHAT_ID_INVALID")
		}
		res, err := r.deps.Channels.EditMessage(ctx, botID, domain.EditChannelMessageRequest{
			UserID: botID, ChannelID: peer.ID, ID: messageID,
			Message: text, Entities: append([]domain.MessageEntity(nil), entities...), Media: edit.media,
			MentionUserIDs: r.mentionUserIDsFromDomain(ctx, botID, text, entities),
			SetReplyMarkup: edit.setReplyMarkup, ReplyMarkup: edit.replyMarkup,
			SetRichMessage: edit.media != nil, EditDate: now,
		})
		if err != nil {
			return domain.Message{}, channelEditErr(err)
		}
		r.enqueueChannelEditMessageFanout(ctx, botID, res)
		return botAPIMessageFromChannel(botID, res.Message), nil
	}
	if r.deps.Messages == nil {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	res, err := r.deps.Messages.EditMessage(ctx, botID, domain.EditMessageRequest{
		OwnerUserID: botID, Peer: peer, ID: messageID,
		Message: text, Entities: append([]domain.MessageEntity(nil), entities...), Media: edit.media,
		EditDate:       now,
		SetReplyMarkup: edit.setReplyMarkup, ReplyMarkup: edit.replyMarkup,
		SetRichMessage: edit.media != nil,
	})
	if err != nil {
		return domain.Message{}, messageEditErr(err)
	}
	r.enqueueBotAPIPrivateEditUpdatesAsync(ctx, res)
	self := res.Self()
	if self.Message.ID == 0 {
		return domain.Message{}, errors.New("MESSAGE_ID_INVALID")
	}
	return self.Message, nil
}

// BotAPIEditMessageReplyMarkup 实现 editMessageReplyMarkup；replyMarkup 为 nil 即清空。
func (r *Router) BotAPIEditMessageReplyMarkup(ctx context.Context, botID, chatID int64, messageID int, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	return r.botAPIEditStored(ctx, botID, chatID, messageID, botAPIStoredEdit{setReplyMarkup: true, replyMarkup: replyMarkup})
}

// BotAPIEditInlineMessageReplyMarkup 实现 inline_message_id 形式的 editMessageReplyMarkup。
func (r *Router) BotAPIEditInlineMessageReplyMarkup(ctx context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, replyMarkup *domain.MessageReplyMarkup) (bool, error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return false, errors.New("BOT_INVALID")
	}
	req := &tg.MessagesEditInlineBotMessageRequest{ID: tgInputBotInlineMessageID(inlineMessageID)}
	req.SetReplyMarkup(botAPIEditReplyMarkup(true, replyMarkup))
	return r.onMessagesEditInlineBotMessage(WithUserID(ctx, botID), req)
}

// BotAPIEditMessageMedia 实现 editMessageMedia（chat_id+message_id）。
func (r *Router) BotAPIEditMessageMedia(ctx context.Context, botID, chatID int64, messageID int, item BotAPIInputMediaItem, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.Message, error) {
	if r == nil || botID == 0 || r.deps.Files == nil {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	media, err := r.botAPIEphemeralMedia(ctx, botID, item.Kind, item.File, item.SecondaryFile)
	if err != nil {
		return domain.Message{}, err
	}
	media.Spoiler = item.Spoiler
	return r.botAPIEditStored(ctx, botID, chatID, messageID, botAPIStoredEdit{
		media: media, caption: item.Caption, entities: item.Entities,
		setReplyMarkup: setReplyMarkup, replyMarkup: replyMarkup,
	})
}

// BotAPIEditInlineMessageMedia 实现 inline_message_id 形式的 editMessageMedia：文件先
// 落库，再以 id 引用交给 messages.editInlineBotMessage。
func (r *Router) BotAPIEditInlineMessageMedia(ctx context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, item BotAPIInputMediaItem, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (bool, error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return false, errors.New("BOT_INVALID")
	}
	if len(item.File.Bytes) > 0 || len(item.SecondaryFile.Bytes) > 0 {
		return false, errors.New("FILE_ID_INVALID")
	}
	media, err := r.botAPIStoredInputMedia(ctx, botID, item)
	if err != nil {
		return false, err
	}
	req := &tg.MessagesEditInlineBotMessageRequest{ID: tgInputBotInlineMessageID(inlineMessageID)}
	req.InvertMedia = item.File.ShowCaptionAboveMedia
	req.SetMedia(media)
	req.SetMessage(item.Caption)
	if len(item.Entities) > 0 {
		req.SetEntities(tgMessageEntities(item.Entities))
	}
	if wire := botAPIEditReplyMarkup(setReplyMarkup, replyMarkup); wire != nil {
		req.SetReplyMarkup(wire)
	}
	return r.onMessagesEditInlineBotMessage(WithUserID(ctx, botID), req)
}

// BotAPIStopPoll 实现 stopPoll：走 TDesktop 的 "Stop poll" 路径关闭投票，需要时再换
// 键盘，最后以机器人视角重读投票（含最终结果）。
func (r *Router) BotAPIStopPoll(ctx context.Context, botID, chatID int64, messageID int, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (domain.MessagePoll, error) {
	if messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return domain.MessagePoll{}, errors.New("MESSAGE_ID_INVALID")
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return domain.MessagePoll{}, errors.New("CHAT_ID_INVALID")
	}
	// 关闭在 store 里是幂等的；官方对已关闭的投票报错，这里先查一次。
	current, err := r.botAPIReloadMessage(ctx, botID, peer, messageID)
	if err != nil {
		return domain.MessagePoll{}, err
	}
	if current.Media == nil || current.Media.Poll == nil {
		return domain.MessagePoll{}, errors.New("MESSAGE_ID_INVALID")
	}
	if current.Media.Poll.Closed {
		return domain.MessagePoll{}, errors.New("MESSAGE_POLL_CLOSED")
	}
	if err := r.botAPIClosePoll(ctx, botID, chatID, messageID); err != nil {
		return domain.MessagePoll{}, err
	}
	if setReplyMarkup {
		if _, err := r.BotAPIEditMessageReplyMarkup(ctx, botID, chatID, messageID, replyMarkup); err != nil {
			return domain.MessagePoll{}, err
		}
	}
	// 编辑结果是落库快照，不含按 viewer 填充的 closed/结果；以读路径重读。
	msg, err := r.botAPIReloadMessage(ctx, botID, peer, messageID)
	if err != nil {
		return domain.MessagePoll{}, err
	}
	if msg.Media == nil || msg.Media.Poll == nil {
		return domain.MessagePoll{}, errors.New("MESSAGE_ID_INVALID")
	}
	return *msg.Media.Poll, nil
}

// BotAPIEditInlineMessageCaption 实现 inline_message_id 形式的 editMessageCaption。
func (r *Router) BotAPIEditInlineMessageCaption(ctx context.Context, botID int64, inlineMessageID domain.BotInlineMessageID, caption string, entities []domain.MessageEntity, setReplyMarkup bool, replyMarkup *domain.MessageReplyMarkup) (bool, error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return false, errors.New("BOT_INVALID")
	}
	req := &tg.MessagesEditInlineBotMessageRequest{ID: tgInputBotInlineMessageID(inlineMessageID)}
	req.SetMessage(caption)
	if len(entities) > 0 {
		req.SetEntities(tgMessageEntities(entities))
	}
	if wire := botAPIEditReplyMarkup(setReplyMarkup, replyMarkup); wire != nil {
		req.SetReplyMarkup(wire)
	}
	return r.onMessagesEditInlineBotMessage(WithUserID(ctx, botID), req)
}

func botAPIValidateMediaReplacement(current domain.Message, next *domain.MessageMedia) error {
	if current.Media != nil && !current.Media.IsZero() && current.Media.Kind != domain.MessageMediaKindPhoto && current.Media.Kind != domain.MessageMediaKindDocument && current.Media.Kind != domain.MessageMediaKindWebPage {
		return errors.New("MEDIA_PREV_INVALID")
	}
	if current.GroupedID == 0 {
		return nil
	}
	kind := func(media *domain.MessageMedia) string {
		if media == nil {
			return ""
		}
		if media.Photo != nil {
			return "visual"
		}
		if media.Document != nil {
			for _, attribute := range media.Document.Attributes {
				if attribute.Kind == domain.DocAttrAnimated {
					return "animation"
				}
			}
			for _, attribute := range media.Document.Attributes {
				if attribute.Kind == domain.DocAttrAudio && !attribute.Voice {
					return "audio"
				}
				if attribute.Kind == domain.DocAttrVideo && !attribute.RoundMessage {
					return "visual"
				}
			}
			return "document"
		}
		return ""
	}
	if previous, replacement := kind(current.Media), kind(next); previous != replacement || replacement == "animation" {
		return errors.New("MEDIA_GROUP_INVALID")
	}
	return nil
}
