package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Bot API 的 sendMediaGroup / sendPoll / sendDice / sendChatAction：组装 TL 请求后
// 以机器人身份调用 MTProto 处理器，再从 Updates 取回落库的消息。

// botAPISendTarget 解析发送目标并组装共有的回复参数。
func (r *Router) botAPISendTarget(ctx context.Context, botID, chatID int64, replyToMessageID int) (tg.InputPeerClass, domain.Peer, tg.InputReplyToClass, error) {
	if r == nil || botID == 0 {
		return nil, domain.Peer{}, nil, errors.New("BOT_INVALID")
	}
	input, peer, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return nil, domain.Peer{}, nil, err
	}
	var reply tg.InputReplyToClass
	if replyToMessageID > 0 {
		reply = &tg.InputReplyToMessage{ReplyToMsgID: replyToMessageID}
	}
	return input, peer, reply, nil
}

// botAPISendOneMedia 以机器人身份发送一条媒体消息并取回它。
func (r *Router) botAPISendOneMedia(ctx context.Context, botID, chatID int64, media tg.InputMediaClass, caption string, entities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int) (domain.Message, error) {
	input, peer, reply, err := r.botAPISendTarget(ctx, botID, chatID, replyToMessageID)
	if err != nil {
		return domain.Message{}, err
	}
	req := &tg.MessagesSendMediaRequest{
		Peer:       input,
		Media:      media,
		Message:    caption,
		RandomID:   randomNonZeroInt64(),
		Silent:     silent,
		Noforwards: protectContent,
	}
	if reply != nil {
		req.SetReplyTo(reply)
	}
	if len(entities) > 0 {
		req.SetEntities(tgMessageEntities(entities))
	}
	if replyMarkup != nil && !replyMarkup.IsZero() {
		req.SetReplyMarkup(tgReplyMarkup(replyMarkup))
	}
	updates, err := r.onMessagesSendMedia(WithUserID(ctx, botID), req)
	if err != nil {
		return domain.Message{}, err
	}
	msgs, err := r.botAPIMessagesFromUpdates(ctx, botID, peer, updates)
	if err != nil {
		return domain.Message{}, err
	}
	if len(msgs) == 0 {
		return domain.Message{}, errors.New("MESSAGE_ID_INVALID")
	}
	return msgs[0], nil
}

// BotAPIInputMediaItem 是一项 Bot API InputMedia。与 botapi.InputMediaItem 是同一个
// 匿名 struct 的别名，因此网关接口两边类型一致而无需新增 domain 类型。
type BotAPIInputMediaItem = struct {
	Kind          string // photo / video / audio / document / animation
	File          domain.BotAPIFileInput
	SecondaryFile domain.BotAPIFileInput
	Caption       string
	Entities      []domain.MessageEntity
	Spoiler       bool
}

// botAPIStoredInputMedia 先把文件落库（上传、URL 抓取或 file_id 查找），再以
// id 引用的 InputMediaPhoto/Document 交给 MTProto 处理器——resolveInputMedia 对
// 引用只按 id 加载，不校验 file_reference。
func (r *Router) botAPIStoredInputMedia(ctx context.Context, botID int64, item BotAPIInputMediaItem) (tg.InputMediaClass, error) {
	if r.deps.Files == nil {
		return nil, errors.New("MEDIA_INVALID")
	}
	media, err := r.botAPIEphemeralMedia(ctx, botID, item.Kind, item.File, item.SecondaryFile)
	if err != nil {
		return nil, err
	}
	switch {
	case media.Photo != nil:
		out := &tg.InputMediaPhoto{ID: &tg.InputPhoto{ID: media.Photo.ID, AccessHash: media.Photo.AccessHash}, Spoiler: item.Spoiler}
		if media.LivePhotoVideo != nil {
			out.SetVideo(&tg.InputDocument{ID: media.LivePhotoVideo.ID, AccessHash: media.LivePhotoVideo.AccessHash})
		}
		return out, nil
	case media.Document != nil:
		out := &tg.InputMediaDocument{ID: &tg.InputDocument{ID: media.Document.ID, AccessHash: media.Document.AccessHash}, Spoiler: item.Spoiler}
		if media.VideoCover != nil {
			out.SetVideoCover(&tg.InputPhoto{ID: media.VideoCover.ID, AccessHash: media.VideoCover.AccessHash})
		}
		if media.VideoTimestamp != 0 {
			out.SetVideoTimestamp(int(media.VideoTimestamp))
		}
		return out, nil
	default:
		return nil, errors.New("MEDIA_INVALID")
	}
}

// BotAPISendMediaGroup 实现 sendMediaGroup：逐项落库后走 messages.sendMultiMedia，
// 由处理器分配共同的 grouped_id。
func (r *Router) BotAPISendMediaGroup(ctx context.Context, botID, chatID int64, items []BotAPIInputMediaItem, silent, protectContent bool, replyToMessageID int) ([]domain.Message, error) {
	input, peer, reply, err := r.botAPISendTarget(ctx, botID, chatID, replyToMessageID)
	if err != nil {
		return nil, err
	}
	req := &tg.MessagesSendMultiMediaRequest{Peer: input, Silent: silent, Noforwards: protectContent}
	if reply != nil {
		req.SetReplyTo(reply)
	}
	for _, item := range items {
		media, err := r.botAPIStoredInputMedia(ctx, botID, item)
		if err != nil {
			return nil, err
		}
		single := tg.InputSingleMedia{Media: media, RandomID: randomNonZeroInt64(), Message: item.Caption}
		if len(item.Entities) > 0 {
			single.SetEntities(tgMessageEntities(item.Entities))
		}
		req.MultiMedia = append(req.MultiMedia, single)
	}
	updates, err := r.onMessagesSendMultiMedia(WithUserID(ctx, botID), req)
	if err != nil {
		return nil, err
	}
	return r.botAPIMessagesFromUpdates(ctx, botID, peer, updates)
}

// botAPIReloadMessage 以机器人视角重读一条消息（复用 Updates 的重读路径）。
func (r *Router) botAPIReloadMessage(ctx context.Context, botID int64, peer domain.Peer, messageID int) (domain.Message, error) {
	msgs, err := r.botAPIMessagesFromUpdates(ctx, botID, peer, &tg.UpdateShortSentMessage{ID: messageID})
	if err != nil {
		return domain.Message{}, err
	}
	if len(msgs) == 0 {
		return domain.Message{}, errors.New("MESSAGE_ID_INVALID")
	}
	return msgs[0], nil
}

// BotAPISendPoll 实现 sendPoll：组装 InputMediaPoll 走 messages.sendMedia；is_closed
// 的投票先发出再立即关闭（与客户端"发送后停止投票"同一路径）。
func (r *Router) BotAPISendPoll(ctx context.Context, botID, chatID int64, poll domain.MessagePoll, correctOptionIDs []int, explanation string, explanationEntities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int) (domain.Message, error) {
	return r.BotAPISendPollWithOptions(ctx, botID, chatID, poll, correctOptionIDs, explanation, explanationEntities, replyMarkup, silent, protectContent, replyToMessageID, domain.BotAPIMessageOptions{})
}

func (r *Router) BotAPISendPollWithOptions(ctx context.Context, botID, chatID int64, poll domain.MessagePoll, correctOptionIDs []int, explanation string, explanationEntities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int, options domain.BotAPIMessageOptions) (domain.Message, error) {
	wire := tg.Poll{
		PublicVoters:   poll.PublicVoters,
		MultipleChoice: poll.MultipleChoice,
		Quiz:           poll.Quiz,
		Question:       tgTextWithEntities(poll.Question, poll.QuestionEntities),
		Answers:        make([]tg.PollAnswerClass, 0, len(poll.Answers)),
	}
	for _, answer := range poll.Answers {
		wire.Answers = append(wire.Answers, &tg.InputPollAnswer{Text: tgTextWithEntities(answer.Text, answer.Entities)})
	}
	if poll.ClosePeriod > 0 {
		wire.SetClosePeriod(poll.ClosePeriod)
	}
	if poll.CloseDate > 0 {
		wire.SetCloseDate(poll.CloseDate)
	}
	media := &tg.InputMediaPoll{Poll: wire}
	if len(correctOptionIDs) > 0 {
		media.SetCorrectAnswers(correctOptionIDs)
	}
	if explanation != "" {
		media.SetSolution(explanation)
		if len(explanationEntities) > 0 {
			media.SetSolutionEntities(tgMessageEntities(explanationEntities))
		}
	}
	msg, err := r.botAPISendOneMediaWithOptions(ctx, botID, chatID, media, "", nil, replyMarkup, silent, protectContent, replyToMessageID, options)
	if err != nil || !poll.Closed {
		return msg, err
	}
	if err := r.botAPIClosePoll(ctx, msg.OwnerUserID, chatID, msg.ID); err != nil {
		return domain.Message{}, err
	}
	closed, err := r.botAPIReloadMessage(ctx, msg.OwnerUserID, msg.Peer, msg.ID)
	if err == nil && options.BusinessConnectionID != "" {
		r.botAPIEnqueueBusinessMessage(ctx, botID, options.BusinessConnectionID, closed, true)
	}
	return closed, err
}

// botAPIClosePoll 走 messages.editMessage + InputMediaPoll(closed)，即 TDesktop 的
// "Stop poll" 路径；处理器只看 closed 标志，poll 其余字段不参与。
func (r *Router) botAPIClosePoll(ctx context.Context, botID, chatID int64, messageID int) error {
	input, _, _, err := r.botAPISendTarget(ctx, botID, chatID, 0)
	if err != nil {
		return err
	}
	req := &tg.MessagesEditMessageRequest{Peer: input, ID: messageID}
	req.SetMedia(&tg.InputMediaPoll{Poll: tg.Poll{Closed: true}})
	_, err = r.onMessagesEditMessage(WithUserID(ctx, botID), req)
	return err
}

// botAPIChatAction 把官方 sendChatAction 的 action 映射到 TL SendMessageAction。
func botAPIChatAction(action string) (tg.SendMessageActionClass, bool) {
	switch action {
	case "typing":
		return &tg.SendMessageTypingAction{}, true
	case "upload_photo":
		return &tg.SendMessageUploadPhotoAction{}, true
	case "record_video":
		return &tg.SendMessageRecordVideoAction{}, true
	case "upload_video":
		return &tg.SendMessageUploadVideoAction{}, true
	case "record_voice":
		return &tg.SendMessageRecordAudioAction{}, true
	case "upload_voice":
		return &tg.SendMessageUploadAudioAction{}, true
	case "upload_document":
		return &tg.SendMessageUploadDocumentAction{}, true
	case "choose_sticker":
		return &tg.SendMessageChooseStickerAction{}, true
	case "find_location":
		return &tg.SendMessageGeoLocationAction{}, true
	case "record_video_note":
		return &tg.SendMessageRecordRoundAction{}, true
	case "upload_video_note":
		return &tg.SendMessageUploadRoundAction{}, true
	default:
		return nil, false
	}
}

// BotAPISendChatAction 实现 sendChatAction，走 messages.setTyping。
func (r *Router) BotAPISendChatAction(ctx context.Context, botID, chatID int64, action string, messageThreadID int) (bool, error) {
	wire, ok := botAPIChatAction(action)
	if !ok {
		return false, errors.New("ACTION_INVALID")
	}
	input, _, _, err := r.botAPISendTarget(ctx, botID, chatID, 0)
	if err != nil {
		return false, err
	}
	req := &tg.MessagesSetTypingRequest{Peer: input, Action: wire}
	if messageThreadID > 0 {
		req.SetTopMsgID(messageThreadID)
	}
	return r.onMessagesSetTyping(WithUserID(ctx, botID), req)
}

// BotAPISendDice 实现 sendDice；emoji 合法性由 InputMediaDice 的解析校验。
func (r *Router) BotAPISendDice(ctx context.Context, botID, chatID int64, emoji string, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int) (domain.Message, error) {
	return r.botAPISendOneMedia(ctx, botID, chatID, &tg.InputMediaDice{Emoticon: emoji}, "", nil, replyMarkup, silent, protectContent, replyToMessageID)
}
