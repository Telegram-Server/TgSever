package domain

import "errors"

// 频道身份封禁（#137）：群/频道管理员可以封禁一个频道 peer，阻止任何人以这个频道的身份
// 在群里发言（Bot API banChatSenderChat，MTProto channels.editBanned 传频道 peer）。
// 只有"全封"（view_messages）和"解封"两种状态，没有部分限制。

var (
	// ErrChannelBannedRightsInvalid 表示对频道 peer 传了除全封、解封之外的权限组合。
	ErrChannelBannedRightsInvalid = errors.New("channel banned rights invalid")
	// ErrChannelSenderChatInvalid 表示目标就是本群自己，或要封禁的频道不存在、已删除。
	ErrChannelSenderChatInvalid = errors.New("channel sender chat invalid")
)

// ChannelBannedSenderChat 是"某个频道在某个群/频道里被封禁"的一条记录。
type ChannelBannedSenderChat struct {
	// ChannelID 是发生封禁的群/频道。
	ChannelID int64
	// SenderChatID 是被封禁的频道 peer。
	SenderChatID int64
	// BannedByUserID 是执行封禁的管理员（真人或 bot）。
	BannedByUserID int64
	// Date 是封禁时间（unix 秒）。
	Date int
}

// Member 把封禁记录投影成 kicked 列表/单个成员查询/管理日志共用的成员形态：
// SenderChatID 非零、UserID 为零，状态是被踢出且带 view_messages 禁令。
func (b ChannelBannedSenderChat) Member() ChannelMember {
	return ChannelMember{
		ChannelID:     b.ChannelID,
		SenderChatID:  b.SenderChatID,
		InviterUserID: b.BannedByUserID,
		Role:          ChannelRoleMember,
		Status:        ChannelMemberKicked,
		LeftAt:        b.Date,
		BannedRights:  ChannelBannedRights{ViewMessages: true},
	}
}

// SenderChatLeftMember 是频道 peer 未被封禁时的成员形态，供管理日志的另一侧使用。
func SenderChatLeftMember(channelID, senderChatID int64) ChannelMember {
	return ChannelMember{
		ChannelID:    channelID,
		SenderChatID: senderChatID,
		Role:         ChannelRoleMember,
		Status:       ChannelMemberLeft,
	}
}

// SenderChatBanAdminLogEvent 生成频道 peer 封禁/解封的管理日志：与用户被踢出/解除踢出同类，
// 参与者用 SenderChatID 表示频道 peer。record 是封禁记录（解封时是被删除的那条），date 是
// 日志时间（memory 与 postgres 各自按请求时间或当前时间取值后传入）。
func SenderChatBanAdminLogEvent(req EditChannelBannedSenderChatRequest, record ChannelBannedSenderChat, banned bool, date int) ChannelAdminLogEvent {
	bannedMember := record.Member()
	left := SenderChatLeftMember(req.ChannelID, req.SenderChatID)
	event := ChannelAdminLogEvent{ChannelID: req.ChannelID, UserID: req.UserID, Date: date}
	if banned {
		event.Type = ChannelAdminLogParticipantKick
		event.PrevParticipant, event.NewParticipant = &left, &bannedMember
	} else {
		event.Type = ChannelAdminLogParticipantUnkick
		event.PrevParticipant, event.NewParticipant = &bannedMember, &left
	}
	return event
}

// SenderChatBanFromRights 把 channels.editBanned 的权限换算成频道 peer 的目标状态：
// view_messages 为全封（其余位与期限忽略，view_messages 已蕴含全部禁令）；
// 全部为空为解封（期限忽略）；其他组合返回 ErrChannelBannedRightsInvalid。
func SenderChatBanFromRights(rights ChannelBannedRights) (banned bool, err error) {
	if rights.ViewMessages {
		return true, nil
	}
	rights.UntilDate = 0
	if rights == (ChannelBannedRights{}) {
		return false, nil
	}
	return false, ErrChannelBannedRightsInvalid
}

// EditChannelBannedSenderChatRequest 封禁或解封一个频道 peer。
type EditChannelBannedSenderChatRequest struct {
	UserID       int64
	ChannelID    int64
	SenderChatID int64
	// BannedRights 只接受全封（ViewMessages）或全空（解封），见 SenderChatBanFromRights。
	BannedRights ChannelBannedRights
	Date         int
}

// EditChannelBannedSenderChatResult 描述一次频道 peer 封禁/解封。
type EditChannelBannedSenderChatResult struct {
	Channel Channel
	// Banned 是操作后的状态。
	Banned bool
	// Changed 为 false 表示重复操作（已封再封、未封解封），不写管理日志。
	Changed bool
	// Ban 是封禁后的当前记录；解封时是被删除的那条记录（重复解封时为零值）。
	Ban ChannelBannedSenderChat
}

// ChannelBannedSenderChatList 是频道 peer 封禁记录的一页，按 SenderChatID 升序。
type ChannelBannedSenderChatList struct {
	Bans []ChannelBannedSenderChat
	// SenderChats 是本页被封禁频道的基础行（非个性化），供投影 chats 使用。
	SenderChats []Channel
	// Count 是全部封禁记录数（不受分页影响）。
	Count int
}
