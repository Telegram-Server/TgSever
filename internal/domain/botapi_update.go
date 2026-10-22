package domain

import (
	"errors"
	"time"
)

// BotAPIUpdateKind is the Bot API delivery shape for a queued update.
type BotAPIUpdateKind string

const (
	BotAPIUpdateMessage          BotAPIUpdateKind = "message"
	BotAPIUpdateEditedMessage    BotAPIUpdateKind = "edited_message"
	BotAPIUpdateCallbackQuery    BotAPIUpdateKind = "callback_query"
	BotAPIUpdatePreCheckoutQuery BotAPIUpdateKind = "pre_checkout_query"
	// BotAPIUpdateMyChatMember 是机器人自己在群/频道里的成员状态变化，默认投递。
	BotAPIUpdateMyChatMember BotAPIUpdateKind = "my_chat_member"
	// BotAPIUpdateChatMember 是群里其他成员的状态变化。官方规则：只有 allowed_updates
	// 显式写了 chat_member 才投递，空列表/未设置都不包含它。
	BotAPIUpdateChatMember           BotAPIUpdateKind = "chat_member"
	BotAPIUpdateMessageReaction      BotAPIUpdateKind = "message_reaction"
	BotAPIUpdateMessageReactionCount BotAPIUpdateKind = "message_reaction_count"
	BotAPIUpdateChatJoinRequest      BotAPIUpdateKind = "chat_join_request"
)

// 成员更新只服务 Bot API 队列投影，不写账号 pts。
const (
	UpdateEventBotMyChatMember    UpdateEventType = "bot_my_chat_member"
	UpdateEventBotChatMember      UpdateEventType = "bot_chat_member"
	UpdateEventBotChatJoinRequest UpdateEventType = "bot_chat_join_request"
)

// BotAPIUpdateKindNeedsOptIn 报告该类更新是否只在 allowed_updates 显式列出时投递。
func BotAPIUpdateKindNeedsOptIn(kind BotAPIUpdateKind) bool {
	return kind == BotAPIUpdateChatMember || kind == BotAPIUpdateMessageReaction || kind == BotAPIUpdateMessageReactionCount
}

// BotChatMemberSnapshot 是成员状态的最小快照：只保留投影 ChatMember 需要的字段，
// 不带已读位置、历史边界等账号私有状态。
type BotChatMemberSnapshot struct {
	UserID        int64               `json:"user_id"`
	InviterUserID int64               `json:"inviter_user_id,omitempty"`
	Role          ChannelMemberRole   `json:"role"`
	Status        ChannelMemberStatus `json:"status"`
	AdminRights   ChannelAdminRights  `json:"admin_rights"`
	BannedRights  ChannelBannedRights `json:"banned_rights"`
	Rank          string              `json:"rank,omitempty"`
}

// NewBotChatMemberSnapshot 从完整成员记录截取快照。
func NewBotChatMemberSnapshot(member ChannelMember) BotChatMemberSnapshot {
	return BotChatMemberSnapshot{
		UserID: member.UserID, InviterUserID: member.InviterUserID,
		Role: member.Role, Status: member.Status,
		AdminRights: member.AdminRights, BannedRights: member.BannedRights,
		Rank: member.Rank,
	}
}

// ChannelMember 还原成 apiChatMember 能投影的成员记录。
func (s BotChatMemberSnapshot) ChannelMember(channelID int64) ChannelMember {
	return ChannelMember{
		ChannelID: channelID, UserID: s.UserID, InviterUserID: s.InviterUserID,
		Role: s.Role, Status: s.Status,
		AdminRights: s.AdminRights, BannedRights: s.BannedRights,
		Rank: s.Rank,
	}
}

// BotChatMemberUpdated 是官方 ChatMemberUpdated 的协议无关载荷，my_chat_member 与
// chat_member 共用。它没有消息可回读，入队时整体落库。
type BotChatMemberUpdated struct {
	Chat        Peer                  `json:"chat"`
	ActorUserID int64                 `json:"actor_user_id"`
	Date        int                   `json:"date"`
	Old         BotChatMemberSnapshot `json:"old"`
	New         BotChatMemberSnapshot `json:"new"`
	// Viewer 是收件机器人入队时自己的成员状态，用来按它的视角算 can_be_edited。
	// 旧行没有该字段时为零值，投影退化为 can_be_edited=false。
	Viewer BotChatMemberSnapshot `json:"viewer,omitzero"`
	Invite *ChannelInvite        `json:"invite,omitempty"`
	// ViaJoinRequest：管理员批准了入群申请。
	ViaJoinRequest bool `json:"via_join_request,omitempty"`
	// ViaChatFolderInviteLink：通过 chatlist（文件夹）邀请链接加入。
	ViaChatFolderInviteLink bool `json:"via_chat_folder_invite_link,omitempty"`
}

// ValidateFor 校验载荷能否投给 botUserID：my_chat_member 的当事人必须是该机器人，
// chat_member 的当事人必须是别人；新旧状态必须不同。
func (u BotChatMemberUpdated) ValidateFor(botUserID int64, kind BotAPIUpdateKind) error {
	if botUserID <= 0 || u.Chat.Type != PeerTypeChannel || u.Chat.ID <= 0 || u.ActorUserID <= 0 || u.Date <= 0 ||
		u.New.UserID <= 0 || u.Old.UserID != u.New.UserID || u.Old == u.New ||
		(u.Viewer.UserID != 0 && u.Viewer.UserID != botUserID) {
		return ErrBotChatMemberUpdateInvalid
	}
	switch kind {
	case BotAPIUpdateMyChatMember:
		if u.New.UserID != botUserID {
			return ErrBotChatMemberUpdateInvalid
		}
	case BotAPIUpdateChatMember:
		if u.New.UserID == botUserID {
			return ErrBotChatMemberUpdateInvalid
		}
	default:
		return ErrBotChatMemberUpdateInvalid
	}
	return nil
}

// ErrBotChatMemberUpdateInvalid 表示成员更新载荷与其队列行不一致。
var ErrBotChatMemberUpdateInvalid = errors.New("invalid bot api chat member update")

// ValidateBotAPIChatMemberRequest 校验成员类入队请求的形状：peer 是群/频道、没有消息、
// 载荷与 peer 一致。非成员类请求不得携带成员载荷。两套 store 共用。
func ValidateBotAPIChatMemberRequest(req EnqueueBotAPIUpdateRequest) error {
	if req.Kind != BotAPIUpdateMyChatMember && req.Kind != BotAPIUpdateChatMember {
		if req.ChatMember != nil {
			return ErrBotChatMemberUpdateInvalid
		}
		return nil
	}
	if req.ChatMember == nil || req.Peer.Type != PeerTypeChannel || req.Peer != req.ChatMember.Chat ||
		req.MessageID != 0 || req.SourcePts != 0 || req.Callback != nil || req.PreCheckout != nil || req.Ephemeral != nil {
		return ErrBotChatMemberUpdateInvalid
	}
	return req.ChatMember.ValidateFor(req.BotUserID, req.Kind)
}

// BotPreCheckoutQuery is the protocol-neutral payload shared by the MTProto
// updateBotPrecheckoutQuery and the HTTP Bot API PreCheckoutQuery projection.
//
// It asks the bot to confirm an order before any Stars move. The payer is held in
// the payment RPC for PreCheckoutTimeout; if the bot has not answered by then the
// payment is failed, which is what makes this a real gate and not a notification.
type BotPreCheckoutQuery struct {
	ID        int64 `json:"id"`
	BotUserID int64 `json:"bot_user_id"`
	UserID    int64 `json:"user_id"`
	// Currency and TotalAmount are what the payer is about to be charged. The bot
	// is expected to verify them against its own order state.
	Currency    string `json:"currency"`
	TotalAmount int64  `json:"total_amount"`
	// Payload is the invoice_payload from sendInvoice, the bot's own order key.
	Payload string `json:"invoice_payload"`
	// ShippingOptionID is reserved: telesrv settles Stars only and models no
	// shipping, so it is always empty.
	ShippingOptionID string `json:"shipping_option_id,omitempty"`
}

// BotPreCheckoutAnswer is what the bot sends back. Error is the message the payer
// sees when OK is false.
type BotPreCheckoutAnswer struct {
	OK    bool
	Error string
}

// BotCallbackQuery is the protocol-neutral payload shared by MTProto
// updateBotCallbackQuery and the HTTP Bot API CallbackQuery projection.
type BotCallbackQuery struct {
	ID            int64
	BotUserID     int64
	UserID        int64
	Peer          Peer
	MessageID     int
	ChatInstance  int64
	Data          []byte
	GameShortName string
	TopMessageID  int `json:"-"`
	InlineMessage *BotInlineMessageID
	// ClientSession is available only to an in-process service bot. It must not
	// become part of a durable/public Bot API CallbackQuery payload.
	ClientSession ClientSessionMetadata `json:"-"`
}

// BotAPIEphemeralPayload is a self-contained 24-hour Bot API queue snapshot.
// Ordinary queued messages are reloaded from their durable message tables;
// ephemeral messages have no such table and therefore travel in this explicit
// envelope instead of overloading SourcePts or an ordinary message id. The
// public shape deliberately cannot represent random IDs, payload hashes,
// auth-key/session identifiers, or the originating device.
type BotAPIEphemeralPayload struct {
	Message BotAPIEphemeralMessage
	ReplyTo *BotAPIEphemeralMessage `json:",omitempty"`
}

type BotAPIEphemeralMessage struct {
	ID                 int
	Peer               Peer
	SenderUserID       int64
	ReceiverUserID     int64
	Date               int
	EditDate           int
	TopMessageID       int
	ReplyToEphemeralID int
	Content            EphemeralContent
	Version            uint64
	ExpiresAt          time.Time
}

func NewBotAPIEphemeralPayload(message EphemeralMessage) *BotAPIEphemeralPayload {
	payload := &BotAPIEphemeralPayload{Message: publicBotAPIEphemeralMessage(message)}
	if message.BotAPIReply != nil {
		reply := publicBotAPIEphemeralMessage(*message.BotAPIReply)
		payload.ReplyTo = &reply
	}
	return payload
}

func publicBotAPIEphemeralMessage(message EphemeralMessage) BotAPIEphemeralMessage {
	return BotAPIEphemeralMessage{
		ID: message.ID, Peer: message.Peer,
		SenderUserID: message.SenderUserID, ReceiverUserID: message.ReceiverUserID,
		Date: message.Date, EditDate: message.EditDate,
		TopMessageID: message.TopMessageID, ReplyToEphemeralID: message.ReplyToEphemeralID,
		Content: message.Content, Version: message.Version, ExpiresAt: message.ExpiresAt,
	}
}

func (m BotAPIEphemeralMessage) EphemeralMessage() EphemeralMessage {
	return EphemeralMessage{
		ID: m.ID, Peer: m.Peer,
		SenderUserID: m.SenderUserID, ReceiverUserID: m.ReceiverUserID,
		Date: m.Date, EditDate: m.EditDate,
		TopMessageID: m.TopMessageID, ReplyToEphemeralID: m.ReplyToEphemeralID,
		Content: m.Content, Version: m.Version, ExpiresAt: m.ExpiresAt,
	}
}

func (p BotAPIEphemeralPayload) EphemeralMessage() EphemeralMessage {
	message := p.Message.EphemeralMessage()
	if p.ReplyTo != nil {
		reply := p.ReplyTo.EphemeralMessage()
		message.BotAPIReply = &reply
	}
	return message
}

func (p BotAPIEphemeralPayload) Validate() error {
	if err := p.Message.Validate(); err != nil {
		return err
	}
	if p.Message.ReplyToEphemeralID == 0 {
		if p.ReplyTo != nil {
			return ErrEphemeralInvalid
		}
		return nil
	}
	if p.ReplyTo == nil || p.ReplyTo.Validate() != nil || p.ReplyTo.ID != p.Message.ReplyToEphemeralID ||
		p.ReplyTo.Peer != p.Message.Peer || p.ReplyTo.Date > p.Message.Date ||
		!sameEphemeralParticipantPair(p.Message.SenderUserID, p.Message.ReceiverUserID, p.ReplyTo.SenderUserID, p.ReplyTo.ReceiverUserID) {
		return ErrEphemeralInvalid
	}
	return nil
}

func sameEphemeralParticipantPair(firstSender, firstReceiver, secondSender, secondReceiver int64) bool {
	return (firstSender == secondSender && firstReceiver == secondReceiver) ||
		(firstSender == secondReceiver && firstReceiver == secondSender)
}

func (m BotAPIEphemeralMessage) Expired(now time.Time) bool {
	return !m.ExpiresAt.IsZero() && !now.Before(m.ExpiresAt)
}

func (m BotAPIEphemeralMessage) Validate() error {
	date := time.Unix(int64(m.Date), 0)
	if m.ID <= 0 || m.ID > MaxMessageBoxID || m.Peer.Type != PeerTypeChannel || m.Peer.ID <= 0 ||
		m.SenderUserID <= 0 || m.ReceiverUserID <= 0 || m.SenderUserID == m.ReceiverUserID ||
		m.Date <= 0 || m.Version == 0 || m.ExpiresAt.IsZero() || !m.ExpiresAt.After(date) ||
		m.ExpiresAt.Sub(date) > EphemeralMessageRetention+time.Second ||
		(m.EditDate != 0 && m.EditDate < m.Date) || m.TopMessageID < 0 || m.TopMessageID > MaxMessageBoxID ||
		m.ReplyToEphemeralID < 0 || m.ReplyToEphemeralID > MaxMessageBoxID || m.ReplyToEphemeralID == m.ID {
		return ErrEphemeralInvalid
	}
	return ValidateEphemeralContent(m.Content)
}

// BotInlineMessageID is the domain-only shape of inputBotInlineMessageID64.
// It can be projected both to MTProto and to Bot API's opaque
// inline_message_id without leaking tg types into the store boundary.
type BotInlineMessageID struct {
	DCID       int
	OwnerID    int64
	ID         int
	AccessHash int64
}

// BotAPIUpdate is a durable Bot API update cursor. ID is the Bot API update_id
// and is global across all bots, matching Telegram Bot API's monotonic offset
// contract without reusing MTProto pts from user/channel logs.
type BotAPIUpdate struct {
	ID        int64
	BotUserID int64
	Kind      BotAPIUpdateKind
	Peer      Peer
	MessageID int
	SourcePts int
	Date      int
	Callback  *BotCallbackQuery
	// PreCheckout carries a pre-checkout query. It has no message of its own, so
	// Peer and MessageID stay zero and the projection reads only this field.
	PreCheckout *BotPreCheckoutQuery
	Ephemeral   *BotAPIEphemeralPayload
	// ChatMember 携带 my_chat_member / chat_member 载荷；Peer 是群/频道，MessageID 为零。
	ChatMember  *BotChatMemberUpdated
	Reaction    *BotMessageReactionUpdated
	JoinRequest *BotChatJoinRequest
	Object      *BotAPIObject
}

// EnqueueBotAPIUpdateRequest describes a message-like update that should be
// delivered to one bot via getUpdates.
type EnqueueBotAPIUpdateRequest struct {
	BotUserID   int64
	Kind        BotAPIUpdateKind
	Peer        Peer
	MessageID   int
	SourcePts   int
	Date        int
	Callback    *BotCallbackQuery
	PreCheckout *BotPreCheckoutQuery
	Ephemeral   *BotAPIEphemeralPayload
	// ChatMember 携带 my_chat_member / chat_member 载荷；Peer 是群/频道，MessageID 为零。
	ChatMember  *BotChatMemberUpdated
	Reaction    *BotMessageReactionUpdated
	JoinRequest *BotChatJoinRequest
	Object      *BotAPIObject
}
