package domain

// IsChannelAdminBotAudience 报告成员记录是否让这个 bot 收到群里其他成员的变动（#146）：
// 仍在群里的管理员或群主。入群申请（#149）与消息反应（#150）的收件规则在它之上细分。
// 是否为 bot 由调用方判定。
func IsChannelAdminBotAudience(member ChannelMember) bool {
	return member.UserID != 0 && member.Status == ChannelMemberActive &&
		(member.Role == ChannelRoleAdmin || member.Role == ChannelRoleCreator)
}

// WithChannelAdminBotAudience 把成员变动的 qts 记录候选（BotChannelParticipantChange /
// BotChannelParticipantChanges 生成，每条发给变动的成员本人）复制给群里每个管理员 bot：
// TDLib 把 user_id 不是自己的 updateChannelParticipant 转成 chat_member（#146）。
//
// adminBots 是变动之后群里的管理员 bot 成员记录（调用方在同一事务里读出）；变动的成员本人
// 不重复收。返回的列表里原候选保持原位，复制紧随其后，保证每个 bot 的 qts 顺序与变动顺序一致。
func WithChannelAdminBotAudience(changes []BotUpdate, adminBots []ChannelMember) []BotUpdate {
	if len(changes) == 0 || len(adminBots) == 0 {
		return changes
	}
	out := make([]BotUpdate, 0, len(changes)*(1+len(adminBots)))
	for _, change := range changes {
		out = append(out, change)
		p := change.ChannelParticipant
		if p == nil {
			continue
		}
		for _, bot := range adminBots {
			if !IsChannelAdminBotAudience(bot) || bot.UserID == p.Current.UserID {
				continue
			}
			copied := *p
			copied.Viewer = bot
			out = append(out, BotUpdate{BotUserID: bot.UserID, Date: change.Date, ChannelParticipant: &copied})
		}
	}
	return out
}
