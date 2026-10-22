package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// setBotDefaultAdminRights 是 bots.setBotGroupDefaultAdminRights（forChannels=false）与
// bots.setBotBroadcastDefaultAdminRights（forChannels=true）的共同实现：只有 bot 本身能调用
// （否则 USER_BOT_REQUIRED），值未变回 RIGHTS_NOT_MODIFIED。写入后失效该 bot 的 userFull
// 投影与频道 full 的 bot_info 缓存，其他用户下一次 getFullUser 即看到新值。
func (r *Router) setBotDefaultAdminRights(ctx context.Context, forChannels bool, rights tg.ChatAdminRights) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	if err := r.writeBotDefaultAdminRights(ctx, botID, forChannels, domainChannelAdminRights(rights)); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Router) writeBotDefaultAdminRights(ctx context.Context, botID int64, forChannels bool, rights domain.ChannelAdminRights) error {
	if _, err := r.deps.Bots.SetDefaultAdminRights(ctx, botID, forChannels, rights); err != nil {
		switch {
		case errors.Is(err, domain.ErrBotAdminRightsNotModified):
			return rightsNotModifiedErr()
		case errors.Is(err, domain.ErrBotNotFound):
			return userBotRequiredErr()
		default:
			return internalErr()
		}
	}
	r.invalidateChannelFullBotInfoCache()
	r.invalidateRPCProjectionForUser(botID)
	return nil
}

// applyBotDefaultAdminRights 给 bot 的 userFull 填 bot_group_admin_rights /
// bot_broadcast_admin_rights；未设置（零值）的那组不下发。
func (r *Router) applyBotDefaultAdminRights(ctx context.Context, full *tg.UserFull, botID int64) {
	if r.deps.Bots == nil {
		return
	}
	profile, found, err := r.deps.Bots.BotInfo(ctx, botID)
	if err != nil || !found {
		return
	}
	if profile.GroupAdminRights != (domain.ChannelAdminRights{}) {
		full.SetBotGroupAdminRights(tgBotDefaultAdminRights(profile.GroupAdminRights))
	}
	if profile.BroadcastAdminRights != (domain.ChannelAdminRights{}) {
		full.SetBotBroadcastAdminRights(tgBotDefaultAdminRights(profile.BroadcastAdminRights))
	}
}

// tgBotDefaultAdminRights 投影 bot 的建议权限。与成员管理员投影不同，other 位如实
// 反映 ManageChat，不强制置位：客户端据此预勾选"管理群组"。
func tgBotDefaultAdminRights(rights domain.ChannelAdminRights) tg.ChatAdminRights {
	out := tgChatAdminRights(rights)
	out.Other = rights.ManageChat
	return out
}

// BotAPISetMyDefaultAdministratorRights 是 Bot API setMyDefaultAdministratorRights 的网关：
// 以 bot 身份调用 bots.setBotGroup/BroadcastDefaultAdminRights，继承处理器的校验与缓存失效。
// 官方 Bot API 设置相同的值也返回 True，所以这里把 RIGHTS_NOT_MODIFIED 当成功。
func (r *Router) BotAPISetMyDefaultAdministratorRights(ctx context.Context, botID int64, forChannels bool, rights domain.ChannelAdminRights) error {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return errors.New("BOT_INVALID")
	}
	botCtx := WithUserID(ctx, botID)
	var err error
	if forChannels {
		_, err = r.onBotsSetBotBroadcastDefaultAdminRights(botCtx, tgBotDefaultAdminRights(rights))
	} else {
		_, err = r.onBotsSetBotGroupDefaultAdminRights(botCtx, tgBotDefaultAdminRights(rights))
	}
	if tgerr.Is(err, "RIGHTS_NOT_MODIFIED") {
		return nil
	}
	return err
}

// BotAPIGetMyDefaultAdministratorRights 是 getMyDefaultAdministratorRights 的网关；
// 未设置时返回零值（Bot API 层投影为全部 false 的 ChatAdministratorRights）。
func (r *Router) BotAPIGetMyDefaultAdministratorRights(ctx context.Context, botID int64, forChannels bool) (domain.ChannelAdminRights, error) {
	if r == nil || botID == 0 || r.deps.Bots == nil {
		return domain.ChannelAdminRights{}, errors.New("BOT_INVALID")
	}
	profile, found, err := r.deps.Bots.BotInfo(ctx, botID)
	if err != nil {
		return domain.ChannelAdminRights{}, internalErr()
	}
	if !found {
		return domain.ChannelAdminRights{}, errors.New("BOT_INVALID")
	}
	if forChannels {
		return profile.BroadcastAdminRights, nil
	}
	return profile.GroupAdminRights, nil
}
