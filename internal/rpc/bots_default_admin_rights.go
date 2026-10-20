package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
)

func (r *Router) setBotDefaultAdminRights(ctx context.Context, broadcast bool, rights tg.ChatAdminRights) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	changed, err := r.deps.Bots.SetBotDefaultAdminRights(ctx, botID, broadcast, domainChannelAdminRights(rights))
	if err != nil {
		return false, internalErr()
	}
	if !changed {
		return false, rightsNotModifiedErr()
	}
	r.invalidateRPCProjectionForUser(botID)
	return true, nil
}

func (r *Router) applyBotDefaultAdminRights(ctx context.Context, botID int64, full *tg.UserFull) error {
	if r.deps.Bots == nil {
		return nil
	}
	profile, found, err := r.deps.Bots.BotInfo(ctx, botID)
	if err != nil {
		return internalErr()
	}
	if !found {
		return nil
	}
	group := tgChatAdminRights(profile.GroupAdminRights)
	group.Other = profile.GroupAdminRights.ManageChat
	broadcast := tgChatAdminRights(profile.BroadcastAdminRights)
	broadcast.Other = profile.BroadcastAdminRights.ManageChat
	full.SetBotGroupAdminRights(group)
	full.SetBotBroadcastAdminRights(broadcast)
	return nil
}
