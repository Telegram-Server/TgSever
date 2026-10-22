package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"telesrv/internal/domain"
)

func (r *Router) commerceSubscriptionsStatus(ctx context.Context, req *tg.PaymentsGetStarsSubscriptionsRequest) (*tg.PaymentsStarsStatus, error) {
	if req == nil || len(req.Offset) > domain.MaxStarsTransactionsOffsetBytes {
		return nil, inputRequestInvalidErr()
	}
	userID, owner, err := r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	if owner.Type != domain.PeerTypeUser || owner.ID != userID {
		return nil, peerIDInvalidErr()
	}
	if r.deps.BotCommerce == nil || r.deps.Stars == nil {
		return nil, notImplementedErr()
	}
	balance, err := r.deps.Stars.GetBalance(ctx, userID)
	if err != nil {
		return nil, starsErr(err)
	}
	rows, err := r.deps.BotCommerce.UserSubscriptions(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := emptyStarsStatus(&tg.StarsAmount{Amount: balance.Balance})
	subscriptions := make([]tg.StarsSubscription, 0, 100)
	started := req.Offset == ""
	var missing int64
	var userIDs, channelIDs []int64
	for _, v := range rows {
		if !started {
			if v.ID == req.Offset {
				started = true
			}
			continue
		}
		needsFunds := !v.Active && !v.Canceled && !v.UserCanceled
		if req.MissingBalance && !needsFunds {
			continue
		}
		if len(subscriptions) == 100 {
			out.SetSubscriptionsNextOffset(subscriptions[len(subscriptions)-1].ID)
			break
		}
		offer, err := r.deps.BotCommerce.InvoiceLink(ctx, v.Slug)
		if err != nil {
			return nil, err
		}
		item := tg.StarsSubscription{ID: v.ID, Peer: &tg.PeerUser{UserID: v.BotID}, UntilDate: v.Until, Pricing: tg.StarsSubscriptionPricing{Period: v.Period, Amount: v.Amount}, Canceled: v.Canceled || v.UserCanceled, BotCanceled: v.Canceled, MissingBalance: needsFunds}
		if v.ChannelID > 0 {
			item.Peer = &tg.PeerChannel{ChannelID: v.ChannelID}
			item.SetChatInviteHash(offer.InviteHash)
			channelIDs = append(channelIDs, v.ChannelID)
		} else {
			item.SetTitle(offer.Title)
			item.SetInvoiceSlug(v.Slug)
			userIDs = append(userIDs, v.BotID)
		}
		if needsFunds {
			missing += v.Amount
		}
		subscriptions = append(subscriptions, item)
	}
	if req.Offset != "" && !started {
		return nil, inputRequestInvalidErr()
	}
	out.SetSubscriptions(subscriptions)
	if missing > balance.Balance {
		out.SetSubscriptionsMissingBalance(missing - balance.Balance)
	}
	out.Users = r.tgUsersForIDs(ctx, userID, userIDs)
	out.Chats = r.tgChatsForChannelIDs(ctx, userID, channelIDs)
	return out, nil
}

func (r *Router) registerCommercePayments(d *tlprofile.Dispatcher) {
	registerRPC[*tg.PaymentsChangeStarsSubscriptionRequest](d, tlprofile.SemanticMethodPaymentsChangeStarsSubscription, func(ctx context.Context, req *tg.PaymentsChangeStarsSubscriptionRequest) (any, error) {
		if req == nil || r.deps.BotCommerce == nil {
			return false, inputRequestInvalidErr()
		}
		userID, owner, err := r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
		if err != nil {
			return false, err
		}
		if owner.Type != domain.PeerTypeUser || owner.ID != userID {
			return false, peerIDInvalidErr()
		}
		canceled, present := req.GetCanceled()
		if !present {
			return false, inputRequestInvalidErr()
		}
		err = r.deps.BotCommerce.ChangeUserSubscription(ctx, userID, req.SubscriptionID, canceled, int(r.clock.Now().Unix()))
		if errors.Is(err, domain.ErrCommerceInvalid) || errors.Is(err, domain.ErrCommerceNotFound) {
			return false, subscriptionIDInvalidErr()
		}
		if err != nil {
			return false, internalErr()
		}
		return true, nil
	})
	registerRPC[*tg.PaymentsFulfillStarsSubscriptionRequest](d, tlprofile.SemanticMethodPaymentsFulfillStarsSubscription, func(ctx context.Context, req *tg.PaymentsFulfillStarsSubscriptionRequest) (any, error) {
		if req == nil || r.deps.BotCommerce == nil {
			return false, inputRequestInvalidErr()
		}
		userID, owner, err := r.starGiftLedgerOwnerForPeer(ctx, req.Peer)
		if err != nil {
			return false, err
		}
		if owner.Type != domain.PeerTypeUser || owner.ID != userID {
			return false, peerIDInvalidErr()
		}
		result, err := r.deps.BotCommerce.FulfillSubscription(ctx, userID, req.SubscriptionID, int(r.clock.Now().Unix()))
		if errors.Is(err, domain.ErrCommerceInvalid) || errors.Is(err, domain.ErrCommerceNotFound) {
			return false, subscriptionIDInvalidErr()
		}
		if err != nil {
			return false, channelInviteErr(err)
		}
		r.addOnlineChannelMemberships(result.Channel.ID, channelMemberUserIDs(result.Members)...)
		r.prepareChannelInviteViewers(ctx, &result)
		r.pushChannelUpdates(ctx, userID, result.Channel.ID, result.Recipients, func(viewerID int64) *tg.Updates { return r.channelOperationUpdates(ctx, viewerID, result) })
		r.enqueueBotAPIMemberUpdates(ctx, botAPIJoinedMemberChanges(userID, result, int(r.clock.Now().Unix()))...)
		return true, nil
	})
	registerRPC[*tg.PaymentsBotCancelStarsSubscriptionRequest](d, tlprofile.SemanticMethodPaymentsBotCancelStarsSubscription, func(ctx context.Context, req *tg.PaymentsBotCancelStarsSubscriptionRequest) (any, error) {
		botID, err := r.callerBotID(ctx)
		if err != nil {
			return false, err
		}
		if req == nil {
			return false, inputRequestInvalidErr()
		}
		user, found, err := r.userFromInput(ctx, botID, req.UserID)
		if err != nil {
			return false, err
		}
		if !found {
			return false, userIDInvalidErr()
		}
		return r.BotAPIEditUserStarSubscription(ctx, botID, user.ID, req.ChargeID, !req.Restore)
	})
}
