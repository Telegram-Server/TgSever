package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

// resolvePersonalChannel is shared by profile projection and bot history reads;
// only an active creator may expose the association.
func (r *Router) resolvePersonalChannel(ctx context.Context, viewerUserID, ownerUserID, channelID int64) (domain.ChannelView, bool) {
	if channelID == 0 || r.deps.Channels == nil {
		return domain.ChannelView{}, false
	}
	owner, err := r.deps.Channels.ResolveChannel(ctx, ownerUserID, channelID)
	if err != nil || owner.Self.Status != domain.ChannelMemberActive || owner.Self.Role != domain.ChannelRoleCreator {
		return domain.ChannelView{}, false
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, viewerUserID, channelID)
	if err != nil || view.Channel.ID == 0 || view.Channel.Deleted || !view.Channel.Broadcast || view.Channel.Megagroup || view.Channel.Username == "" {
		return domain.ChannelView{}, false
	}
	return view, true
}

func (r *Router) onMessagesGetPersonalChannelHistory(ctx context.Context, req *tg.MessagesGetPersonalChannelHistoryRequest) (tg.MessagesMessagesClass, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if err := validateMessageReadBounds(req.Limit, 0, req.MaxID, req.MinID); err != nil {
		return nil, err
	}
	owner, found, err := r.userFromInput(ctx, botID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	if !found {
		return nil, userIDInvalidErr()
	}
	if owner.Deleted {
		return &tg.MessagesMessages{}, nil
	}
	view, found := r.resolvePersonalChannel(ctx, botID, owner.ID, owner.PersonalChannelID)
	if !found {
		return &tg.MessagesMessages{}, nil
	}
	if err := r.checkFrozenChannelParticipants(ctx, botID, view.Channel.ID); err != nil {
		return nil, err
	}
	// ChannelHistoryFilter.MaxID is inclusive; the protocol's max_id is
	// exclusive, so use the ordinary backward-page anchor as well.
	history, err := r.deps.Channels.GetPersonalChannelHistory(ctx, botID, owner.ID, domain.ChannelHistoryFilter{ChannelID: view.Channel.ID, Limit: req.Limit, OffsetID: req.MaxID, MaxID: req.MaxID, MinID: req.MinID, Hash: req.Hash})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	if req.Hash != 0 && history.Hash == req.Hash {
		return &tg.MessagesMessagesNotModified{Count: history.Count}, nil
	}
	return r.tgChannelHistoryMessages(ctx, botID, r.enrichChannelHistory(ctx, botID, history)), nil
}
