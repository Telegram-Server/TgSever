package botapi

import "telesrv/internal/domain"

// apiChatMember 把一条频道成员记录投影成官方 ChatMember 的六种形态之一。判定顺序与
// MTProto 的 tgChannelParticipant 一致：封禁（含 view_messages 禁令）先于
// 退出，退出先于角色。canBeEdited 只对 administrator 有意义，由调用方按查看者算出。
func apiChatMember(member domain.ChannelMember, user domain.User, canBeEdited bool) map[string]any {
	if user.ID == 0 {
		user.ID = member.UserID
	}
	out := map[string]any{"user": apiUser(user)}
	banned := member.BannedRights
	switch {
	case member.IsKicked():
		out["status"] = "kicked"
		out["until_date"] = banned.UntilDate
	case member.Status == domain.ChannelMemberLeft || member.Guest:
		out["status"] = "left"
	case member.Role == domain.ChannelRoleCreator:
		out["status"] = "creator"
		out["is_anonymous"] = member.AdminRights.Anonymous
		if member.Rank != "" {
			out["custom_title"] = member.Rank
		}
	case member.Role == domain.ChannelRoleAdmin:
		out["status"] = "administrator"
		out["can_be_edited"] = canBeEdited
		for key, value := range apiAdministratorRights(member.AdminRights) {
			out[key] = value
		}
		if member.Rank != "" {
			out["custom_title"] = member.Rank
		}
	case member.Status == domain.ChannelMemberBanned || hasBannedRight(banned):
		out["status"] = "restricted"
		out["is_member"] = member.Status == domain.ChannelMemberActive
		for key, value := range apiChatPermissions(banned) {
			out[key] = value
		}
		out["until_date"] = banned.UntilDate
	default:
		out["status"] = "member"
	}
	if (out["status"] == "member" || out["status"] == "restricted") && member.Rank != "" {
		out["tag"] = member.Rank
	}
	return out
}

// hasBannedRight 报告是否有任一禁止位；UntilDate 只是期限，不算禁令。
func hasBannedRight(b domain.ChannelBannedRights) bool {
	b.UntilDate = 0
	return b != domain.ChannelBannedRights{}
}
