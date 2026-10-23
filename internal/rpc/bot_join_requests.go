package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// projectBotUpdate 投影一条 bot qts 记录：入群申请（#149）需要公开链接前缀，在这里处理，
// 其余类型交给 tgBotUpdate。
func (r *Router) projectBotUpdate(update domain.BotUpdate, view *domain.ChannelView) (tg.UpdateClass, []int64, tg.ChatClass) {
	if update.ChatInviteRequester != nil {
		return tgBotChatInviteRequester(update, view, r.cfg.PublicBaseURL)
	}
	return tgBotUpdate(update, view)
}

// botUpdateChannelID 返回记录所属的频道；不属于频道的记录为 0。
func botUpdateChannelID(update domain.BotUpdate) int64 {
	switch {
	case update.ChannelParticipant != nil:
		return update.ChannelParticipant.Channel.ID
	case update.ChatInviteRequester != nil:
		return update.ChatInviteRequester.Channel.ID
	case update.MessageReaction != nil:
		return update.MessageReaction.Channel.ID
	case update.MessageReactions != nil:
		return update.MessageReactions.Channel.ID
	}
	return 0
}

// tgBotChatInviteRequester 把入群申请记录投影成 updateBotChatInviteRequester。公开群的入群
// 申请没有邀请链接，invite 为 chatInvitePublicJoinRequests。
func tgBotChatInviteRequester(update domain.BotUpdate, view *domain.ChannelView, publicBaseURL string) (tg.UpdateClass, []int64, tg.ChatClass) {
	p := update.ChatInviteRequester
	bot := update.BotUserID
	var invite tg.ExportedChatInviteClass = &tg.ChatInvitePublicJoinRequests{}
	if p.Invite.InviteID != 0 {
		invite = tgExportedChannelInvite(p.Invite, publicBaseURL)
	}
	out := &tg.UpdateBotChatInviteRequester{
		Peer:   &tg.PeerChannel{ChannelID: p.Channel.ID},
		Date:   update.Date,
		UserID: p.RequesterUserID,
		About:  p.About,
		Invite: invite,
		Qts:    update.Qts,
	}
	var chat tg.ChatClass
	if view != nil && !view.Forbidden && view.Channel.ID == p.Channel.ID {
		self := view.Self
		chat = tgChannelChat(bot, view.Channel, &self)
	} else {
		self := p.Admin
		chat = tgChannelChat(bot, p.Channel, &self)
	}
	return out, []int64{p.RequesterUserID, p.Invite.AdminUserID}, chat
}
