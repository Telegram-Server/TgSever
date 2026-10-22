package rpc

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Bot API forwardMessage(s) / copyMessage(s)。转发与不改内容的复制都走
// messages.forwardMessages 处理器（复制 = drop_author），以机器人身份继承来源可见性、
// no-forwards 保护、转发来源隐私与更新扇出。

// BotAPIForwardMessages 以机器人身份把 fromChatID 里的消息转发到 chatID。
func (r *Router) BotAPIForwardMessages(ctx context.Context, botID, chatID, fromChatID int64, messageIDs []int, silent, protectContent bool) ([]domain.Message, error) {
	return r.botAPIForward(ctx, botID, chatID, fromChatID, messageIDs, botAPIForwardOptions{silent: silent, protectContent: protectContent})
}

// BotAPICopyMessages 复制消息（不带转发头）。
func (r *Router) BotAPICopyMessages(ctx context.Context, botID, chatID, fromChatID int64, messageIDs []int, removeCaption, silent, protectContent bool) ([]domain.Message, error) {
	return r.botAPIForward(ctx, botID, chatID, fromChatID, messageIDs, botAPIForwardOptions{
		silent: silent, protectContent: protectContent, copyMode: true, removeCaption: removeCaption,
	})
}

// BotAPICopyMessageWithOptions copies visible source content through the same
// authorized reply/business/thread/broadcast path as an ordinary outgoing message.
func (r *Router) BotAPICopyMessageWithOptions(ctx context.Context, botID, chatID, fromChatID int64, messageID int, setCaption bool, caption string, entities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int, options domain.BotAPIMessageOptions) (domain.Message, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return domain.Message{}, err
	}
	_, fromPeer, err := r.botAPIInputPeer(ctx, botID, fromChatID)
	if err != nil {
		return domain.Message{}, err
	}
	source, found, err := r.botAPIReplySource(ctx, botID, fromPeer, messageID)
	if err != nil {
		return domain.Message{}, err
	}
	if !found {
		return domain.Message{}, messageIDInvalidErr()
	}
	if !botAPISourceCopyable(botAPISource{id: source.ID, service: source.Action != nil || botAPIMediaIsService(source.Media), media: source.Media}) {
		return domain.Message{}, messageIDInvalidErr()
	}
	if source.NoForwards || source.Media != nil && (source.Media.TTLSeconds > 0 || source.Media.PaidMedia != nil) {
		return domain.Message{}, chatForwardsRestrictedErr()
	}
	if err := r.botAPICheckPrivateNoForwards(ctx, botID, fromPeer); err != nil {
		return domain.Message{}, err
	}
	body, sourceEntities := source.Body, source.Entities
	if setCaption && source.Media != nil {
		body, sourceEntities = caption, entities
	}
	return r.botAPISendContent(ctx, botID, chatID, body, sourceEntities, cloneInlineMedia(source.Media), domain.CloneRichMessage(source.RichMessage), replyMarkup, silent, protectContent, replyToMessageID, 0, options)
}

// botAPIForwardOptions 是转发/复制的选项。copyMode 即 drop_author，并按复制规则过滤源消息。
type botAPIForwardOptions struct {
	silent, protectContent bool
	copyMode               bool
	replyToMessageID       int
	// removeCaption 即 drop_media_captions，仍走 forwardMessages，相册分组得以保留。
	removeCaption bool
	// 以下任一生效时 forwardMessages 做不到，改为以机器人身份重新发送源内容。
	setCaption  bool
	caption     string
	entities    []domain.MessageEntity
	replyMarkup *domain.MessageReplyMarkup
}

func (o botAPIForwardOptions) resend() bool {
	return o.copyMode && (o.setCaption || o.replyMarkup != nil)
}

// botAPIForward 组装 messages.forwardMessages 请求并取回目标会话里的新消息。
func (r *Router) botAPIForward(ctx context.Context, botID, chatID, fromChatID int64, ids []int, opts botAPIForwardOptions) ([]domain.Message, error) {
	if r == nil || botID == 0 {
		return nil, errors.New("BOT_INVALID")
	}
	if len(ids) == 0 {
		return nil, errors.New("MESSAGE_IDS_EMPTY")
	}
	toInput, toPeer, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return nil, err
	}
	if opts.replyMarkup != nil {
		if err := domain.ValidateReplyMarkup(opts.replyMarkup); err != nil {
			return nil, replyMarkupErr(err)
		}
		if err := r.validateReplyMarkupForPeer(ctx, botID, toPeer, opts.replyMarkup); err != nil {
			return nil, err
		}
	}
	fromInput, fromPeer, err := r.botAPIInputPeer(ctx, botID, fromChatID)
	if err != nil {
		return nil, err
	}
	sources, err := r.botAPISourceMessages(ctx, botID, fromPeer, ids)
	if err != nil {
		return nil, err
	}
	// 官方语义：找不到或不能转发的消息跳过；全部被跳过才报错。
	usable := func(src botAPISource) bool { return !src.service }
	if opts.copyMode {
		usable = botAPISourceCopyable
	}
	ids, err = botAPIUsableSourceIDs(sources, usable)
	if err != nil {
		return nil, err
	}
	if opts.resend() {
		return r.botAPIResendCopies(ctx, botID, fromPeer, toPeer, sources, ids, opts)
	}
	randomIDs := make([]int64, len(ids))
	for i := range randomIDs {
		randomIDs[i] = randomNonZeroInt64()
	}
	req := &tg.MessagesForwardMessagesRequest{
		FromPeer:   fromInput,
		ToPeer:     toInput,
		ID:         append([]int(nil), ids...),
		RandomID:   randomIDs,
		Silent:     opts.silent,
		Noforwards: opts.protectContent,
		DropAuthor: opts.copyMode,
		// 官方语义：drop_media_captions 只在 drop_author 时生效。
		DropMediaCaptions: opts.copyMode && opts.removeCaption,
	}
	if opts.replyToMessageID > 0 {
		req.SetReplyTo(&tg.InputReplyToMessage{ReplyToMsgID: opts.replyToMessageID})
	}
	updates, err := r.onMessagesForwardMessages(WithUserID(ctx, botID), req)
	if err != nil {
		return nil, err
	}
	return r.botAPIMessagesFromUpdates(ctx, botID, toPeer, updates)
}

// botAPIResendCopies 按 ids 顺序把源内容以机器人身份重新发送：用于替换说明文字
// 或附加按钮。转发处理器里的 no-forwards 校验在这里要自己做。
func (r *Router) botAPIResendCopies(ctx context.Context, botID int64, fromPeer, toPeer domain.Peer, sources []botAPISource, ids []int, opts botAPIForwardOptions) ([]domain.Message, error) {
	if err := r.botAPICheckPrivateNoForwards(ctx, botID, fromPeer); err != nil {
		return nil, err
	}
	byID := make(map[int]botAPISource, len(sources))
	for _, src := range sources {
		byID[src.id] = src
	}
	for _, id := range ids {
		if byID[id].noForwards {
			return nil, chatForwardsRestrictedErr()
		}
	}
	out := make([]domain.Message, 0, len(ids))
	for _, id := range ids {
		src := byID[id]
		body, entities := src.body, src.entities
		// 说明文字只属于媒体消息；纯文本消息保留正文。
		if opts.setCaption && forwardMediaHasCaption(src.media) {
			body, entities = opts.caption, opts.entities
		}
		msg, err := r.botAPISendCopy(ctx, botID, toPeer, body, entities, src.media, opts)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, nil
}

// botAPICheckPrivateNoForwards 对应处理器里私聊级别的"禁止转发"开关。
func (r *Router) botAPICheckPrivateNoForwards(ctx context.Context, botID int64, fromPeer domain.Peer) error {
	if fromPeer.Type != domain.PeerTypeUser || fromPeer.ID == botID {
		return nil
	}
	svc, ok := r.deps.Messages.(PrivateNoForwardsService)
	if !ok {
		return nil
	}
	state, err := svc.GetPrivateNoForwards(ctx, botID, fromPeer.ID)
	if err != nil {
		return err
	}
	if state.Enabled() {
		return chatForwardsRestrictedErr()
	}
	return nil
}

// botAPISendCopy 复用 Bot API 发送路径（与 BotAPISendMedia 相同）写一条副本。
func (r *Router) botAPISendCopy(ctx context.Context, botID int64, toPeer domain.Peer, body string, entities []domain.MessageEntity, media *domain.MessageMedia, opts botAPIForwardOptions) (domain.Message, error) {
	if utf8.RuneCountInString(body) > domain.MaxMessageTextLength {
		return domain.Message{}, errors.New("MESSAGE_TOO_LONG")
	}
	if media != nil {
		cloned := *media
		media = &cloned
	}
	entities = append([]domain.MessageEntity(nil), entities...)
	var reply *domain.MessageReply
	if opts.replyToMessageID > 0 {
		reply = &domain.MessageReply{Peer: toPeer, MessageID: opts.replyToMessageID}
	}
	if toPeer.Type == domain.PeerTypeChannel {
		return r.botAPISendChannelMessage(ctx, botID, toPeer.ID, body, entities, media, nil, opts.replyMarkup, opts.silent, opts.protectContent, reply, 0)
	}
	if r.deps.Messages == nil {
		return domain.Message{}, errors.New("BOT_INVALID")
	}
	res, err := r.deps.Messages.SendPrivateText(ctx, botID, domain.SendPrivateTextRequest{
		SenderUserID:    botID,
		RecipientUserID: toPeer.ID,
		RandomID:        randomNonZeroInt64(),
		Message:         body,
		Entities:        entities,
		Media:           media,
		Silent:          opts.silent,
		NoForwards:      opts.protectContent,
		ReplyTo:         reply,
		Date:            int(time.Now().Unix()),
		ReplyMarkup:     opts.replyMarkup,
	})
	if err != nil {
		return domain.Message{}, err
	}
	return res.SenderMessage, nil
}

// botAPISource 是机器人视角下的一条源消息。
type botAPISource struct {
	id         int
	service    bool
	noForwards bool
	body       string
	entities   []domain.MessageEntity
	media      *domain.MessageMedia
}

// botAPISourceMessages 以机器人身份读取 fromPeer 里的源消息：群/频道按机器人的成员
// 视角，私聊按机器人自己的消息盒（只认属于该会话的 id）。看不到的 id 不出现在结果里，
// 结果保持 ids 的顺序。
func (r *Router) botAPISourceMessages(ctx context.Context, botID int64, fromPeer domain.Peer, ids []int) ([]botAPISource, error) {
	out := make([]botAPISource, 0, len(ids))
	switch fromPeer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return nil, errors.New("BOT_INVALID")
		}
		history, err := r.deps.Channels.GetMessages(ctx, botID, fromPeer.ID, ids)
		if err != nil {
			return nil, botAPIChannelSendErr(err)
		}
		byID := make(map[int]domain.ChannelMessage, len(history.Messages))
		for _, msg := range history.Messages {
			byID[msg.ID] = msg
		}
		for _, id := range ids {
			msg, ok := byID[id]
			if !ok {
				continue
			}
			out = append(out, botAPISource{
				id:         id,
				service:    msg.Action != nil || botAPIMediaIsService(msg.Media),
				noForwards: msg.NoForwards || history.Channel.NoForwards,
				body:       msg.Body,
				entities:   msg.Entities,
				media:      msg.Media,
			})
		}
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return nil, errors.New("BOT_INVALID")
		}
		list, err := r.deps.Messages.GetMessages(ctx, botID, ids)
		if err != nil {
			return nil, err
		}
		byID := make(map[int]domain.Message, len(list.Messages))
		for _, msg := range list.Messages {
			if msg.Peer == fromPeer && !msg.Deleted {
				byID[msg.ID] = msg
			}
		}
		for _, id := range ids {
			msg, ok := byID[id]
			if !ok {
				continue
			}
			out = append(out, botAPISource{
				id:         id,
				service:    botAPIMediaIsService(msg.Media),
				noForwards: msg.NoForwards,
				body:       msg.Body,
				entities:   msg.Entities,
				media:      msg.Media,
			})
		}
	default:
		return nil, errors.New("CHAT_ID_INVALID")
	}
	return out, nil
}

// botAPISourceCopyable 按官方 copyMessage 规则：服务消息、发票、抽奖不能复制。
func botAPISourceCopyable(src botAPISource) bool {
	if src.service {
		return false
	}
	if m := src.media; m != nil {
		switch {
		case m.Kind == domain.MessageMediaKindInvoice, m.Invoice != nil, m.ProductInvoice != nil,
			m.Kind == domain.MessageMediaKindGiveaway, m.Giveaway != nil:
			return false
		}
	}
	return true
}

func botAPIMediaIsService(media *domain.MessageMedia) bool {
	return media != nil && (media.Kind == domain.MessageMediaKindService || media.ServiceAction != nil)
}

// botAPIUsableSourceIDs 过滤出 usable 的源 id，全部被过滤时报 MESSAGE_ID_INVALID。
// 受保护消息不在这里跳过：交给处理器整体拒绝（CHAT_FORWARDS_RESTRICTED）。
func botAPIUsableSourceIDs(sources []botAPISource, usable func(botAPISource) bool) ([]int, error) {
	ids := make([]int, 0, len(sources))
	for _, src := range sources {
		if usable(src) {
			ids = append(ids, src.id)
		}
	}
	if len(ids) == 0 {
		return nil, messageIDInvalidErr()
	}
	return ids, nil
}
