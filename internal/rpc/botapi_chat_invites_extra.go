package rpc

import (
	"context"
	"errors"
	"strconv"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) BotAPISetChatSenderBan(ctx context.Context, botID, chatID, senderChatID int64, banned bool) error {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	peer, ok := botAPIPeerFromChatID(senderChatID)
	if !ok || peer.Type != domain.PeerTypeChannel {
		return errors.New("CHAT_ID_INVALID")
	}
	_, err = r.onChannelsEditBanned(WithUserID(ctx, botID), &tg.ChannelsEditBannedRequest{Channel: channel, Participant: &tg.InputPeerChannel{ChannelID: peer.ID}, BannedRights: tg.ChatBannedRights{ViewMessages: banned}})
	return err
}
func (r *Router) BotAPISetChatStickerSet(ctx context.Context, botID, chatID int64, name string) error {
	channel, _, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return err
	}
	var set tg.InputStickerSetClass = &tg.InputStickerSetEmpty{}
	if name != "" {
		set = &tg.InputStickerSetShortName{ShortName: name}
	}
	_, err = r.onChannelsSetStickers(WithUserID(ctx, botID), &tg.ChannelsSetStickersRequest{Channel: channel, Stickerset: set})
	return err
}
func (r *Router) BotAPISetChatMemberTag(ctx context.Context, botID, chatID, userID int64, tag string) error {
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return err
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, botID, peer.ChannelID)
	if err != nil {
		return channelAdminErr(err)
	}
	if view.Self.Role != domain.ChannelRoleCreator && (view.Self.Role != domain.ChannelRoleAdmin || !view.Self.AdminRights.ManageRanks) {
		return errors.New("CHAT_ADMIN_REQUIRED")
	}
	member, err := r.deps.Channels.GetParticipant(ctx, botID, peer.ChannelID, userID)
	if err != nil {
		return channelParticipantErr(err)
	}
	if member.Status != domain.ChannelMemberActive || member.Role != domain.ChannelRoleMember {
		return errors.New("USER_ADMIN_INVALID")
	}
	user, err := r.botAPIInputUser(ctx, botID, userID)
	if err != nil {
		return err
	}
	_, err = r.onMessagesEditChatParticipantRank(WithUserID(ctx, botID), &tg.MessagesEditChatParticipantRankRequest{Peer: peer, Participant: &tg.InputPeerUser{UserID: user.UserID, AccessHash: user.AccessHash}, Rank: tag})
	return err
}
func (r *Router) botAPIInviteChannel(ctx context.Context, botID, chatID int64) (domain.ChannelView, error) {
	_, view, err := r.botAPIInputChannel(ctx, botID, chatID)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if view.Self.Status != domain.ChannelMemberActive || (view.Self.Role != domain.ChannelRoleCreator && (view.Self.Role != domain.ChannelRoleAdmin || !view.Self.AdminRights.InviteUsers)) {
		return domain.ChannelView{}, errors.New("CHAT_ADMIN_REQUIRED")
	}
	return view, nil
}
func (r *Router) botAPIInviteURL(invite domain.ChannelInvite) domain.ChannelInvite {
	invite.Link = publicLinkWithBaseURL(r.cfg.PublicBaseURL, "+"+invite.Hash)
	return invite
}
func (r *Router) BotAPIExportChatInviteLink(ctx context.Context, botID, chatID int64) (domain.ChannelInvite, error) {
	view, err := r.botAPIInviteChannel(ctx, botID, chatID)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	res, err := r.deps.Channels.ExportInvite(ctx, botID, domain.ExportChannelInviteRequest{ChannelID: view.Channel.ID, LegacyRevokePermanent: true, Date: int(r.clock.Now().Unix())})
	if err != nil {
		return domain.ChannelInvite{}, channelInviteErr(err)
	}
	r.invalidateRPCProjectionForChannel(view.Channel.ID)
	return r.botAPIInviteURL(res.Invite), nil
}
func (r *Router) BotAPICreateChatInviteLink(ctx context.Context, botID, chatID int64, p domain.ExportChannelInviteRequest) (domain.ChannelInvite, error) {
	view, err := r.botAPIInviteChannel(ctx, botID, chatID)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	p.UserID = botID
	p.ChannelID = view.Channel.ID
	p.Date = int(r.clock.Now().Unix())
	p.LegacyRevokePermanent = false
	res, err := r.deps.Channels.ExportInvite(ctx, botID, p)
	if err != nil {
		return domain.ChannelInvite{}, channelInviteErr(err)
	}
	r.invalidateRPCProjectionForChannel(view.Channel.ID)
	return r.botAPIInviteURL(res.Invite), nil
}
func (r *Router) botAPIOwnInvite(ctx context.Context, botID, chatID int64, link string) (domain.ChannelInvite, error) {
	view, err := r.botAPIInviteChannel(ctx, botID, chatID)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	hash, err := channelInviteHashFromLink(link)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	invite, err := r.deps.Channels.GetExportedInvite(ctx, botID, domain.GetChannelInviteRequest{ChannelID: view.Channel.ID, Hash: hash})
	if err != nil {
		return domain.ChannelInvite{}, channelInviteErr(err)
	}
	if invite.AdminUserID != botID {
		return domain.ChannelInvite{}, errors.New("INVITE_LINK_NOT_OWNED")
	}
	if r.deps.BotCommerce != nil {
		offer, lookupErr := r.deps.BotCommerce.SubscriptionInvite(ctx, invite.Hash)
		if lookupErr == nil {
			invite.SubscriptionPeriod = offer.SubscriptionPeriod
			invite.SubscriptionPrice = offer.Amount
		} else if !errors.Is(lookupErr, domain.ErrCommerceNotFound) {
			return domain.ChannelInvite{}, lookupErr
		}
	}
	return invite, nil
}
func (r *Router) BotAPIEditChatInviteLink(ctx context.Context, botID, chatID int64, link string, p domain.EditChannelInviteRequest) (domain.ChannelInvite, error) {
	invite, err := r.botAPIOwnInvite(ctx, botID, chatID, link)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	if invite.Permanent {
		return domain.ChannelInvite{}, errors.New("INVITE_LINK_IS_PRIMARY")
	}
	if invite.SubscriptionPeriod > 0 && (p.HasExpireDate || p.HasUsageLimit || p.HasRequestNeeded) {
		return domain.ChannelInvite{}, errors.New("BAD_REQUEST")
	}
	p.ChannelID = invite.ChannelID
	p.Hash = invite.Hash
	p.UserID = botID
	p.Date = int(r.clock.Now().Unix())
	p.Revoked = false
	res, err := r.deps.Channels.EditExportedInvite(ctx, botID, p)
	if err != nil {
		return domain.ChannelInvite{}, channelInviteErr(err)
	}
	res.Invite.SubscriptionPeriod = invite.SubscriptionPeriod
	res.Invite.SubscriptionPrice = invite.SubscriptionPrice
	r.invalidateRPCProjectionForChannel(invite.ChannelID)
	return r.botAPIInviteURL(res.Invite), nil
}
func (r *Router) BotAPIRevokeChatInviteLink(ctx context.Context, botID, chatID int64, link string) (domain.ChannelInvite, error) {
	invite, err := r.botAPIOwnInvite(ctx, botID, chatID, link)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	res, err := r.deps.Channels.EditExportedInvite(ctx, botID, domain.EditChannelInviteRequest{ChannelID: invite.ChannelID, Hash: invite.Hash, Revoked: true, Date: int(r.clock.Now().Unix())})
	if err != nil {
		return domain.ChannelInvite{}, channelInviteErr(err)
	}
	res.Invite.SubscriptionPeriod = invite.SubscriptionPeriod
	res.Invite.SubscriptionPrice = invite.SubscriptionPrice
	r.invalidateRPCProjectionForChannel(invite.ChannelID)
	return r.botAPIInviteURL(res.Invite), nil
}
func (r *Router) BotAPIProcessChatJoinRequest(ctx context.Context, botID, chatID, userID int64, approved bool) error {
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return err
	}
	user, err := r.botAPIInputUser(ctx, botID, userID)
	if err != nil {
		return err
	}
	_, err = r.onMessagesHideChatJoinRequest(WithUserID(ctx, botID), &tg.MessagesHideChatJoinRequestRequest{Peer: peer, UserID: user, Approved: approved})
	return err
}
func (r *Router) BotAPIAnswerChatJoinRequestQuery(ctx context.Context, botID int64, id, result string) error {
	query, err := strconv.ParseInt(id, 10, 64)
	if err != nil || query <= 0 {
		return errors.New("QUERY_ID_INVALID")
	}
	var outcome tg.JoinChatBotResultClass
	switch result {
	case "approve":
		outcome = &tg.JoinChatBotResultApproved{}
	case "decline":
		outcome = &tg.JoinChatBotResultDeclined{}
	case "queue":
		outcome = &tg.JoinChatBotResultQueued{}
	default:
		return errors.New("QUERY_RESULT_INVALID")
	}
	_, err = r.onBotsSetJoinChatResults(WithUserID(ctx, botID), &tg.BotsSetJoinChatResultsRequest{QueryID: query, Result: outcome})
	return err
}
func (r *Router) BotAPISendChatJoinRequestWebApp(ctx context.Context, botID int64, id, url string) error {
	query, err := strconv.ParseInt(id, 10, 64)
	if err != nil || query <= 0 {
		return errors.New("QUERY_ID_INVALID")
	}
	_, err = r.onBotsSetJoinChatResults(WithUserID(ctx, botID), &tg.BotsSetJoinChatResultsRequest{QueryID: query, Result: &tg.JoinChatBotResultWebView{URL: url}})
	return err
}
