package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) deliverChatJoinRequest(ctx context.Context, res domain.CreateChannelResult) {
	if res.JoinRequest == nil {
		return
	}
	payload := res.JoinRequest.Clone()
	if r.deps.Users != nil {
		if user, err := r.deps.Users.Self(ctx, payload.UserID); err == nil {
			payload.About = user.About
		}
	}
	var invite tg.ExportedChatInviteClass = &tg.ChatInvitePublicJoinRequests{}
	if payload.Invite != nil {
		payload.Invite.Link = publicLinkWithBaseURL(r.cfg.PublicBaseURL, "+"+payload.Invite.Hash)
		invite = r.tgExportedChannelInvite(*payload.Invite)
	}
	for _, admin := range r.botAPIChannelAdminBots(ctx, res.Channel) {
		if admin.Role != domain.ChannelRoleCreator && !admin.AdminRights.InviteUsers {
			continue
		}
		ids := []int64{payload.UserID}
		if payload.Invite != nil {
			ids = append(ids, payload.Invite.AdminUserID)
		}
		r.pushUserUpdates(ctx, admin.UserID, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateBotChatInviteRequester{Peer: &tg.PeerChannel{ChannelID: res.Channel.ID}, Date: payload.Date, UserID: payload.UserID, About: payload.About, Invite: invite}}, Users: r.tgUsersForIDs(ctx, admin.UserID, ids), Chats: []tg.ChatClass{tgChannelChatMin(admin.UserID, res.Channel)}, Date: payload.Date})
		if r.deps.BotAPIUpdates != nil {
			if _, created, err := r.deps.BotAPIUpdates.EnqueueBotAPIUpdate(ctx, domain.EnqueueBotAPIUpdateRequest{BotUserID: admin.UserID, Kind: domain.BotAPIUpdateChatJoinRequest, Peer: payload.Chat, Date: payload.Date, JoinRequest: payload}); err == nil && created {
				r.notifyBotAPIUpdate(admin.UserID)
			}
		}
	}
}
