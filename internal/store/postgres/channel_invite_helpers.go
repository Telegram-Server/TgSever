package postgres

import (
	"telesrv/internal/domain"
)

func canExportChannelInvite(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator ||
		(member.Role == domain.ChannelRoleAdmin && member.AdminRights.InviteUsers)
}

func canManageChannelInvite(member domain.ChannelMember, adminUserID int64) bool {
	return canExportChannelInvite(member) && (member.Role == domain.ChannelRoleCreator || member.UserID == adminUserID)
}
