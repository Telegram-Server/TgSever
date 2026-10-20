package domain

// CheckManageChannelAdmin 实现官方管理员保护：群主可编辑/封禁任何管理员；非群主只能动
// 由自己提拔的在任管理员（在任管理员的 InviterUserID 即提拔人），否则 ErrUserAdminInvalid。
// 目标不是在任管理员、或操作者动自己时不受此约束。
//
// 与 store/memory、store/postgres 中的 checkManageChannelAdmin 规则相同；社区封禁
// 对每个关联频道用它做检查，频道层的调用点之后可统一改到这里。
func CheckManageChannelAdmin(actor, target ChannelMember) error {
	if target.Role != ChannelRoleAdmin || target.Status != ChannelMemberActive {
		return nil
	}
	if actor.Role == ChannelRoleCreator || actor.UserID == target.UserID || target.InviterUserID == actor.UserID {
		return nil
	}
	return ErrUserAdminInvalid
}

// CanDeleteAnyChannelMessage reports whether the actor may delete other authors' messages.
// Callers must first verify access to the channel and active membership.
func CanDeleteAnyChannelMessage(member ChannelMember) bool {
	return member.Role == ChannelRoleCreator || (member.Role == ChannelRoleAdmin && member.AdminRights.DeleteMessages)
}

// CanDeleteChannelMessage shares author/admin deletion rules across storage backends.
func CanDeleteChannelMessage(member ChannelMember, actorUserID, senderUserID int64) bool {
	return senderUserID == actorUserID || CanDeleteAnyChannelMessage(member)
}
