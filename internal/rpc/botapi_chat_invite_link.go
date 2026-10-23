package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Bot API 邀请链接方法。创建/导出走 messages.exportChatInvite，修改/撤销走
// messages.editExportedChatInvite，都以机器人身份调用处理器：权限（群主，或拥有
// InviteUsers / ChangeInfo 的管理员）、长度校验与缓存失效都在那里，见 botapi_bridge.go。

// BotAPICreateChatInviteLink 实现 createChatInviteLink：创建一条额外的邀请链接。
func (r *Router) BotAPICreateChatInviteLink(ctx context.Context, botID, chatID int64, options domain.BotAPIChatInviteLinkOptions) (domain.BotAPIChatInviteLink, error) {
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return domain.BotAPIChatInviteLink{}, err
	}
	req := &tg.MessagesExportChatInviteRequest{Peer: peer}
	if options.Name != nil && *options.Name != "" {
		req.SetTitle(*options.Name)
	}
	if options.ExpireDate != nil && *options.ExpireDate > 0 {
		req.SetExpireDate(*options.ExpireDate)
	}
	if options.MemberLimit != nil && *options.MemberLimit > 0 {
		req.SetUsageLimit(*options.MemberLimit)
	}
	if options.CreatesJoinRequest != nil && *options.CreatesJoinRequest {
		req.SetRequestNeeded(true)
	}
	exported, err := r.onMessagesExportChatInvite(WithUserID(ctx, botID), req)
	if err != nil {
		return domain.BotAPIChatInviteLink{}, err
	}
	return r.botAPIReloadChatInviteLink(ctx, botID, peer.ChannelID, exported)
}

// BotAPIEditChatInviteLink 实现 editChatInviteLink：只把 options 里传入的字段交给处理器，
// 未传的字段保持不变。
func (r *Router) BotAPIEditChatInviteLink(ctx context.Context, botID, chatID int64, link string, options domain.BotAPIChatInviteLinkOptions) (domain.BotAPIChatInviteLink, error) {
	req := &tg.MessagesEditExportedChatInviteRequest{Link: link}
	if options.Name != nil {
		req.SetTitle(*options.Name)
	}
	if options.ExpireDate != nil {
		req.SetExpireDate(*options.ExpireDate)
	}
	if options.MemberLimit != nil {
		req.SetUsageLimit(*options.MemberLimit)
	}
	if options.CreatesJoinRequest != nil {
		req.SetRequestNeeded(*options.CreatesJoinRequest)
	}
	// 两个字段同时传入时的冲突由网关拦下；只传一个时要和链接现有的另一个字段合起来看，
	// 否则分两次编辑就能得到"需要审批又有人数上限"的链接。
	if (options.MemberLimit == nil) != (options.CreatesJoinRequest == nil) {
		if err := r.botAPICheckInviteLimitWithApproval(ctx, botID, chatID, link, options); err != nil {
			return domain.BotAPIChatInviteLink{}, err
		}
	}
	return r.botAPIEditChatInvite(ctx, botID, chatID, req)
}

// botAPICheckInviteLimitWithApproval 拒绝编辑后同时需要审批且有人数上限的链接，与官方一致。
func (r *Router) botAPICheckInviteLimitWithApproval(ctx context.Context, botID, chatID int64, link string, options domain.BotAPIChatInviteLinkOptions) error {
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return err
	}
	hash, err := channelInviteHashFromLink(link)
	if err != nil {
		return err
	}
	current, err := r.deps.Channels.GetExportedInvite(ctx, botID, domain.GetChannelInviteRequest{
		UserID:    botID,
		ChannelID: peer.ChannelID,
		Hash:      hash,
	})
	if err != nil {
		return channelInviteErr(err)
	}
	usageLimit, requestNeeded := current.UsageLimit, current.RequestNeeded
	if options.MemberLimit != nil {
		usageLimit = *options.MemberLimit
	}
	if options.CreatesJoinRequest != nil {
		requestNeeded = *options.CreatesJoinRequest
	}
	if requestNeeded && usageLimit > 0 {
		return errors.New(domain.BotAPIInviteLimitWithApproval)
	}
	return nil
}

// BotAPIRevokeChatInviteLink 实现 revokeChatInviteLink，返回撤销后的链接；撤销主链接时
// 处理器会自动生成新的主链接。
func (r *Router) BotAPIRevokeChatInviteLink(ctx context.Context, botID, chatID int64, link string) (domain.BotAPIChatInviteLink, error) {
	return r.botAPIEditChatInvite(ctx, botID, chatID, &tg.MessagesEditExportedChatInviteRequest{Link: link, Revoked: true})
}

// BotAPIExportChatInviteLink 实现 exportChatInviteLink：用 legacy_revoke_permanent 撤销机器人
// 当前的主链接并生成新的主链接，只返回链接字符串。
func (r *Router) BotAPIExportChatInviteLink(ctx context.Context, botID, chatID int64) (string, error) {
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return "", err
	}
	exported, err := r.onMessagesExportChatInvite(WithUserID(ctx, botID), &tg.MessagesExportChatInviteRequest{
		Peer:                  peer,
		LegacyRevokePermanent: true,
	})
	if err != nil {
		return "", err
	}
	invite, ok := exported.(*tg.ChatInviteExported)
	if !ok {
		return "", errors.New("INVITE_HASH_INVALID")
	}
	return invite.Link, nil
}

func (r *Router) botAPIEditChatInvite(ctx context.Context, botID, chatID int64, req *tg.MessagesEditExportedChatInviteRequest) (domain.BotAPIChatInviteLink, error) {
	peer, err := r.botAPIInputChannelPeer(ctx, botID, chatID)
	if err != nil {
		return domain.BotAPIChatInviteLink{}, err
	}
	req.Peer = peer
	res, err := r.onMessagesEditExportedChatInvite(WithUserID(ctx, botID), req)
	if err != nil {
		return domain.BotAPIChatInviteLink{}, err
	}
	// Replaced 的 Invite 是被撤销的旧链接，NewInvite 是自动生成的新主链接；
	// 官方返回的是被操作的那条。
	var edited tg.ExportedChatInviteClass
	switch res := res.(type) {
	case *tg.MessagesExportedChatInvite:
		edited = res.Invite
	case *tg.MessagesExportedChatInviteReplaced:
		edited = res.Invite
	}
	return r.botAPIReloadChatInviteLink(ctx, botID, peer.ChannelID, edited)
}

// botAPIReloadChatInviteLink 按处理器返回的链接重读 domain 邀请记录，再补上创建者资料。
// 调用时处理器已经提交了修改：重读失败（期间被降权、数据库瞬时故障）不能让机器人以为
// 操作没生效而重试，所以退回用处理器返回的 TL 对象组装结果。
func (r *Router) botAPIReloadChatInviteLink(ctx context.Context, botID, channelID int64, exported tg.ExportedChatInviteClass) (domain.BotAPIChatInviteLink, error) {
	tgInvite, ok := exported.(*tg.ChatInviteExported)
	if !ok {
		return domain.BotAPIChatInviteLink{}, errors.New("INVITE_HASH_INVALID")
	}
	hash, err := channelInviteHashFromLink(tgInvite.Link)
	if err != nil {
		return domain.BotAPIChatInviteLink{}, err
	}
	invite, err := r.deps.Channels.GetExportedInvite(ctx, botID, domain.GetChannelInviteRequest{
		UserID:    botID,
		ChannelID: channelID,
		Hash:      hash,
	})
	if err != nil {
		r.log.Warn("reload invite link for bot api failed; answering from the handler result",
			zap.Int64("bot_user_id", botID), zap.Int64("channel_id", channelID), zap.Error(err))
		invite = domainChannelInviteFromTG(channelID, hash, tgInvite)
	}
	return r.botAPIChatInviteLink(ctx, botID, invite), nil
}

// domainChannelInviteFromTG 把处理器返回的 chatInviteExported 还原成 domain 邀请记录；
// 只缺 InviteID，Bot API 投影用不到它。
func domainChannelInviteFromTG(channelID int64, hash string, invite *tg.ChatInviteExported) domain.ChannelInvite {
	return domain.ChannelInvite{
		ChannelID:      channelID,
		Hash:           hash,
		AdminUserID:    invite.AdminID,
		Title:          invite.Title,
		Permanent:      invite.Permanent,
		Revoked:        invite.Revoked,
		RequestNeeded:  invite.RequestNeeded,
		ExpireDate:     invite.ExpireDate,
		UsageLimit:     invite.UsageLimit,
		UsageCount:     invite.Usage,
		RequestedCount: invite.Requested,
		Date:           invite.Date,
	}
}

// botAPIChatInviteLink 把 domain 邀请记录组装成 Bot API ChatInviteLink 所需的全部数据：
// 对外链接与机器人视角的创建者资料。邀请链接方法与 chat_join_request 更新共用。
func (r *Router) botAPIChatInviteLink(ctx context.Context, botID int64, invite domain.ChannelInvite) domain.BotAPIChatInviteLink {
	return r.botAPIChatInviteLinkWithCreators(ctx, botID, invite, nil)
}

// botAPIChatInviteLinkWithCreators 同 botAPIChatInviteLink；creators 非 nil 时按创建者 id
// 记住查到的资料，一批更新里同一个创建者只查一次。
func (r *Router) botAPIChatInviteLinkWithCreators(ctx context.Context, botID int64, invite domain.ChannelInvite, creators map[int64]domain.User) domain.BotAPIChatInviteLink {
	creator, cached := creators[invite.AdminUserID]
	if !cached {
		creator = domain.User{ID: invite.AdminUserID}
		if r.deps.Users != nil && invite.AdminUserID != 0 {
			if user, found, err := r.deps.Users.ByID(ctx, botID, invite.AdminUserID); err == nil && found {
				creator = user
			}
		}
		if creators != nil {
			creators[invite.AdminUserID] = creator
		}
	}
	link := publicLinkWithBaseURL(r.cfg.PublicBaseURL, "+"+invite.Hash)
	if invite.AdminUserID != botID {
		link = botAPIMaskedInviteLink(link)
	}
	return domain.BotAPIChatInviteLink{
		Invite:  invite,
		Link:    link,
		Creator: creator,
	}
}

// botAPIMaskedInviteLink 按官方 ChatInviteLink 的规则遮住别的管理员创建的链接：hash 的后半段
// 换成 "…"，机器人拿不到能直接使用的链接。
func botAPIMaskedInviteLink(link string) string {
	idx := strings.LastIndex(link, "+")
	if idx < 0 {
		return link
	}
	hash := link[idx+1:]
	return link[:idx+1] + hash[:len(hash)/2] + "…"
}
