package rpc

import (
	"context"
	"fmt"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/app/permissions"
	"telesrv/internal/domain"
)

// RevokeAuthorizationAuthKey is the domain-only hook used by the internal Admin API
// after the auth service has removed the durable authorization/auth_key rows.
func (r *Router) RevokeAuthorizationAuthKey(ctx context.Context, authKeyID [8]byte, userID int64) error {
	if r == nil || authKeyID == ([8]byte{}) {
		return nil
	}
	r.revokeAuthKeySessions(authKeyID)
	if err := r.clearAuthKeyState(ctx, authKeyID); err != nil {
		return err
	}
	if userID != 0 {
		r.discardSecretChatsForAuthKey(ctx, businessAuthKeyInt64(authKeyID), userID)
	}
	return nil
}

// NotifyChannelChanged is the domain-only hook used by the internal Admin API
// after a channel/supergroup base fact changed.
func (r *Router) NotifyChannelChanged(ctx context.Context, ch domain.Channel) error {
	if r == nil || ch.ID == 0 {
		return nil
	}
	r.channelStateMutationUpdates(ctx, ch.CreatorUserID, ch)
	return nil
}

// NotifyStarsBalanceChanged is the domain-only hook used by the internal Admin
// API after the local Stars ledger balance has changed outside a client RPC.
func (r *Router) NotifyStarsBalanceChanged(ctx context.Context, balance domain.StarsBalance) error {
	if r == nil || balance.UserID == 0 {
		return nil
	}
	r.pushUserUpdates(ctx, balance.UserID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateStarsBalance{Balance: &tg.StarsAmount{Amount: balance.Balance}}},
		Date:    int(r.clock.Now().Unix()),
	})
	return nil
}

// NotifyAccountFreezeChanged invalidates target-scoped projections immediately
// and wakes the durable audience nudge worker. Cross-instance cache invalidation
// is also carried by the committed user_visibility read-model notification.
func (r *Router) NotifyAccountFreezeChanged(_ context.Context, freeze domain.AccountFreeze) error {
	if r == nil || freeze.UserID == 0 {
		return nil
	}
	r.applyFrozenPresence(freeze.UserID, freeze.Frozen)
	if freeze.Frozen {
		r.announceFrozenEmptyStatus(freeze.UserID)
	}
	if r.deps.UserProjectionFacts != nil {
		r.deps.UserProjectionFacts.InvalidateAccountFreezeFact(freeze.UserID)
	}
	r.invalidateRPCProjectionForUser(freeze.UserID)
	if r.accountFreezeWake != nil {
		select {
		case r.accountFreezeWake <- struct{}{}:
		default:
		}
	}
	return nil
}

// AdminCreateGroup 是后台“以账号身份建群”的钩子：以 ownerID 的身份建一个超级群，效果与该
// 账号在客户端里 channels.createChannel(megagroup) 相同——走同一个业务入口，建成后同样先
// 登记在线成员关系、再由 announceCreatedChannel 扇出更新；ctx 里没有调用会话，所以推给
// 群主的全部在线会话。
// 账号权限闸门只在 RPC 入口，这里不拦截；但仍为群主求一次建群闸门结果，闸门本会拒绝
// （包括读权限视图失败时的 fail closed）就报告绕过，供审计记录。永久邀请按服务器公开
// 基础 URL 拼成完整链接，生成失败时为空，不影响建群。
func (r *Router) AdminCreateGroup(ctx context.Context, ownerID int64, title string) (domain.AdminCreatedGroup, error) {
	if r == nil || r.deps.Channels == nil {
		return domain.AdminCreatedGroup{}, fmt.Errorf("channels dependency is not configured")
	}
	if ownerID <= 0 {
		return domain.AdminCreatedGroup{}, fmt.Errorf("owner is required")
	}
	if !domain.ValidChannelTitle(title) {
		return domain.AdminCreatedGroup{}, domain.ErrChannelTitleInvalid
	}
	bypassed := r.checkPermission(ctx, ownerID, permissions.Action{Kind: permissions.ActionCreateGroup}) != nil
	res, err := r.deps.Channels.CreateChannel(ctx, ownerID, domain.CreateChannelRequest{
		CreatorUserID: ownerID,
		Title:         title,
		Megagroup:     true,
		Date:          int(r.clock.Now().Unix()),
	})
	if err != nil {
		return domain.AdminCreatedGroup{}, err
	}
	r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
	r.announceCreatedChannel(ctx, ownerID, res)
	out := domain.AdminCreatedGroup{Channel: res.Channel, AccountPermissionBypassed: bypassed}
	if res.PermanentInvite != nil && res.PermanentInvite.Hash != "" {
		out.InviteLink = channelInviteLinkWithBaseURL(r.cfg.PublicBaseURL, res.PermanentInvite.Hash)
	}
	return out, nil
}
