package rpc

import (
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

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
	return out, []int64{p.RequesterUserID, p.Invite.AdminUserID}, botChannelChat(bot, view, p.Channel, p.Admin)
}
