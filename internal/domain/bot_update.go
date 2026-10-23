package domain

// BotUpdate 是 bot qts 序列里的一条记录（#145）。
//
// 每个 bot 账号一条连续的 qts 序列：store 在写入源事件的同一事务里为该 bot 原子地分配
// 下一个 qts。bot 不能用密聊，所以 bot 账号的 updates.getState / getDifference 里的
// qts 完全来自这条序列；普通用户的密聊 qts（按设备计数）与它无关。
//
// 记录保存的是领域层的更新内容，而不是 TL 字节：投影（谁看见什么 chat/user 形态）在读出
// 时按接收者计算，序列化格式不随 Layer 变化。
type BotUpdate struct {
	BotUserID int64
	// Qts 由 store 分配；调用方传入时忽略。
	Qts  int
	Date int
	// ChannelParticipant 是 updateChannelParticipant：bot 在超级群/频道里的成员状态变化。
	ChannelParticipant *BotChannelParticipantUpdate `json:",omitempty"`
	// MessageReaction / MessageReactions 是消息反应变化（#150），见 bot_message_reaction.go。
	MessageReaction  *BotMessageReactionUpdate  `json:",omitempty"`
	MessageReactions *BotMessageReactionsUpdate `json:",omitempty"`
	// ChatInviteRequester 是 updateBotChatInviteRequester：有用户提交了入群申请（#149）。
	ChatInviteRequester *BotChatInviteRequesterUpdate `json:",omitempty"`
	// BotStopped 是 updateBotStopped：用户拉黑或解除拉黑了 bot（#148）。
	BotStopped *BotStoppedUpdate `json:",omitempty"`
}

// BotChannelParticipantUpdate 是一次超级群/频道成员状态变化的内容。
type BotChannelParticipantUpdate struct {
	// Channel 是变化发生时的频道快照。读出时优先用当前频道；bot 已不在私有频道里、
	// 看不到当前频道时用它投影 chat。
	Channel     Channel
	ActorUserID int64
	// Previous 为零值（UserID == 0）表示变化前没有成员记录。
	Previous ChannelMember
	Current  ChannelMember
	// Viewer 是接收记录的管理员 bot 自己当时的成员记录（#146，其他成员的变动）；bot 自己的
	// 变化不填，此时 Current 就是它自己。
	Viewer ChannelMember `json:",omitempty"`
}

// BotUpdatesState 是一个 bot 的 qts 序列状态。
type BotUpdatesState struct {
	// Qts 是已分配的最大 qts；从未分配过为 BotInitialQts。
	Qts int
	// MinRetainedQts 是仍保留的最小 qts；没有保留记录时为 0。
	MinRetainedQts int
}

// BotInitialQts 是还没有任何记录的 bot 的 qts：第一条记录分配 BotInitialQts+1。TDLib 拒收
// qts <= 1 的在线推送（td/td/telegram/UpdatesManager.cpp add_pending_qts_update），序列因此
// 不能从 1 开始。
const BotInitialQts = 1

// MaxBotUpdatesDifference 是一次 getDifference 读取的 bot qts 记录上限；更多时返回
// differenceSlice 让客户端接着拉。
const MaxBotUpdatesDifference = 100

// BotChannelParticipantChange 生成 bot 自己在超级群/频道里成员状态变化对应的 qts 记录：
// 加入、移出、设为或撤销管理员、封禁、限制。参与者看得见的状态（身份、角色、权限、限制、
// 头衔）没有变化时不生成。是否为 bot 由调用方（store，在同一事务内）判定；记录发给
// current.UserID 这个 bot。
func BotChannelParticipantChange(channel Channel, actorUserID int64, previous, current ChannelMember, date int) (BotUpdate, bool) {
	if channel.ID == 0 || current.UserID == 0 {
		return BotUpdate{}, false
	}
	if previous.UserID != 0 && !participantStateChanged(previous, current) {
		return BotUpdate{}, false
	}
	if previous.UserID == 0 && current.Status != ChannelMemberActive && current.Status != ChannelMemberBanned && current.Status != ChannelMemberKicked {
		// 从无记录到 left：对外没有任何变化。
		return BotUpdate{}, false
	}
	return BotUpdate{
		BotUserID: current.UserID,
		Date:      date,
		ChannelParticipant: &BotChannelParticipantUpdate{
			Channel:     channel,
			ActorUserID: actorUserID,
			Previous:    previous,
			Current:     current,
		},
	}, true
}

// BotChannelParticipantChanges 把加入/退出类结果（拉人、加入、退出）里每个成员的变化
// 转成 qts 记录候选；变化前的记录取自 res.Previous（没有则视为此前不是成员）。
func BotChannelParticipantChanges(res CreateChannelResult, actorUserID int64, date int) []BotUpdate {
	out := make([]BotUpdate, 0, len(res.Members))
	for _, member := range res.Members {
		previous := res.Previous[member.UserID]
		if previous.Status == "" {
			previous = ChannelMember{}
		}
		if update, ok := BotChannelParticipantChange(res.Channel, actorUserID, previous, member, date); ok {
			out = append(out, update)
		}
	}
	return out
}

func participantStateChanged(previous, current ChannelMember) bool {
	return previous.Status != current.Status ||
		previous.Role != current.Role ||
		previous.AdminRights != current.AdminRights ||
		previous.BannedRights != current.BannedRights ||
		previous.Rank != current.Rank
}
