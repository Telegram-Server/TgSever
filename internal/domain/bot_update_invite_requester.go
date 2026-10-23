package domain

// BotChatInviteRequesterUpdate 是 updateBotChatInviteRequester（#149）：有用户提交了入群申请，
// 发给群里有邀请权限的管理员 bot，TDLib 转成 chat_join_request。
type BotChatInviteRequesterUpdate struct {
	// Channel 是申请提交时的频道快照。
	Channel Channel
	// Invite 是申请经过的邀请链接快照；公开群的入群申请没有链接，InviteID 为 0。
	Invite          ChannelInvite
	RequesterUserID int64
	// About 是申请人提交申请时的个人简介（Bot API 的 bio）。
	About string
	// Admin 是收件 bot 当时的成员记录，用来投影 bot 看到的 chat。
	Admin ChannelMember
}

// CanReceiveBotJoinRequests 报告成员是否会收到入群申请：在群的群主，或带邀请用户权限的管理员。
func CanReceiveBotJoinRequests(member ChannelMember) bool {
	return IsChannelAdminBotAudience(member) && (member.Role == ChannelRoleCreator || member.AdminRights.InviteUsers)
}

// BotChatInviteRequesterUpdates 为一次新的入群申请生成 qts 记录候选：admins 中每个能收到入群
// 申请的成员一条。是否为 bot 由调用方（store，在写申请记录的同一事务内）判定。
func BotChatInviteRequesterUpdates(channel Channel, invite ChannelInvite, requesterUserID int64, about string, admins []ChannelMember, date int) []BotUpdate {
	if channel.ID == 0 || requesterUserID == 0 {
		return nil
	}
	out := make([]BotUpdate, 0, len(admins))
	for _, admin := range admins {
		if admin.UserID == 0 || admin.UserID == requesterUserID || !CanReceiveBotJoinRequests(admin) {
			continue
		}
		out = append(out, BotUpdate{
			BotUserID: admin.UserID,
			Date:      date,
			ChatInviteRequester: &BotChatInviteRequesterUpdate{
				Channel:         channel,
				Invite:          invite,
				RequesterUserID: requesterUserID,
				About:           about,
				Admin:           admin,
			},
		})
	}
	return out
}
