package rpc

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"telesrv/internal/domain"
)

func (r *Router) BotAPIGetUserGifts(ctx context.Context, botID, userID int64, filter domain.SavedStarGiftFilter) (domain.BotAPIOwnedGiftPage, error) {
	if r == nil || r.deps.Users == nil || userID <= 0 {
		return domain.BotAPIOwnedGiftPage{}, userIDInvalidErr()
	}
	user, found, err := r.deps.Users.ByID(ctx, botID, userID)
	if err != nil {
		return domain.BotAPIOwnedGiftPage{}, internalErr()
	}
	if !found || user.Deleted {
		return domain.BotAPIOwnedGiftPage{}, userIDInvalidErr()
	}
	filter.ExcludeUnsaved, filter.ExcludeSaved = true, false
	return r.botAPIOwnedGifts(ctx, botID, domain.Peer{Type: domain.PeerTypeUser, ID: userID}, filter, false)
}

func (r *Router) BotAPIGetChatGifts(ctx context.Context, botID, chatID int64, filter domain.SavedStarGiftFilter) (domain.BotAPIOwnedGiftPage, error) {
	if chatID > 0 {
		return r.BotAPIGetUserGifts(ctx, botID, chatID, filter)
	}
	owner, ok := botAPIPeerFromChatID(chatID)
	if !ok || owner.Type != domain.PeerTypeChannel || r == nil || r.deps.Channels == nil {
		return domain.BotAPIOwnedGiftPage{}, errors.New("CHAT_ID_INVALID")
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, botID, owner.ID)
	if err != nil {
		return domain.BotAPIOwnedGiftPage{}, channelInvalidErr(err)
	}
	if !view.Self.CanPostChannelMessages() {
		filter.ExcludeUnsaved, filter.ExcludeSaved = true, false
	}
	return r.botAPIOwnedGifts(ctx, botID, owner, filter, false)
}

// business is set only by a caller which has resolved can_view_gifts_and_stars.
// Public user/chat queries never promote themselves to a business owner.
func (r *Router) botAPIOwnedGifts(ctx context.Context, botID int64, owner domain.Peer, filter domain.SavedStarGiftFilter, business bool) (domain.BotAPIOwnedGiftPage, error) {
	if r == nil || r.deps.Gifts == nil || r.deps.Users == nil {
		return domain.BotAPIOwnedGiftPage{}, internalErr()
	}
	known, err := r.botAPIKnownBot(ctx, botID)
	if err != nil {
		return domain.BotAPIOwnedGiftPage{}, internalErr()
	}
	if !known {
		return domain.BotAPIOwnedGiftPage{}, errors.New("BOT_INVALID")
	}
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return domain.BotAPIOwnedGiftPage{}, errors.New("LIMIT_INVALID")
	}
	if len(filter.Offset) > domain.MaxStarGiftsOffsetBytes {
		return domain.BotAPIOwnedGiftPage{}, errors.New("OFFSET_INVALID")
	}
	if filter.Offset != "" {
		valid := false
		if filter.SortByValue {
			_, _, valid = domain.DecodeSavedStarGiftPriceCursor(filter.Offset)
		} else {
			_, valid = domain.DecodeSavedStarGiftListCursor(filter.Offset)
		}
		if !valid {
			return domain.BotAPIOwnedGiftPage{}, errors.New("OFFSET_INVALID")
		}
	}
	filter.Owner, filter.BotAPI = owner, true
	page, err := r.deps.Gifts.ListSavedFiltered(ctx, filter)
	if err != nil {
		return domain.BotAPIOwnedGiftPage{}, internalErr()
	}
	out := domain.BotAPIOwnedGiftPage{Count: page.Count, NextOffset: page.NextOffset, Gifts: make([]domain.BotAPIOwnedGift, 0, len(page.Gifts))}
	giftIDs, uniqueIDs := make([]int64, 0, len(page.Gifts)), make([]int64, 0, len(page.Gifts))
	for _, saved := range page.Gifts {
		if saved.UniqueGiftID == 0 {
			giftIDs = append(giftIDs, saved.GiftID)
		} else if saved.Unique == nil {
			uniqueIDs = append(uniqueIDs, saved.UniqueGiftID)
		}
	}
	availability, err := r.deps.Gifts.CollectibleAvailability(ctx, giftIDs)
	if err != nil {
		return domain.BotAPIOwnedGiftPage{}, internalErr()
	}
	uniques, err := r.deps.Gifts.UniqueByIDs(ctx, uniqueIDs)
	if err != nil {
		return domain.BotAPIOwnedGiftPage{}, internalErr()
	}
	revisions := make(map[int64]domain.StarGift)
	now := int(r.clock.Now().Unix())
	transferGifts, transferStars := false, false
	if business {
		svc, ok := r.accountBusinessAutomation()
		if !ok {
			return domain.BotAPIOwnedGiftPage{}, internalErr()
		}
		connection, found, err := svc.GetConnectedBusinessBot(ctx, owner.ID)
		if err != nil {
			return domain.BotAPIOwnedGiftPage{}, internalErr()
		}
		if !found || connection.BotUserID != botID || connection.Disabled || !connection.Rights.ViewGifts {
			return domain.BotAPIOwnedGiftPage{}, errors.New("BUSINESS_CONNECTION_INVALID")
		}
		transferGifts, transferStars = connection.Rights.TransferAndUpgradeGifts, connection.Rights.TransferStars
	}
	for _, saved := range page.Gifts {
		gift, found := revisions[saved.RevisionID]
		if !found {
			gift, found, err = r.deps.Gifts.GiftRevisionByID(ctx, saved.RevisionID)
			if err != nil || !found {
				return domain.BotAPIOwnedGiftPage{}, internalErr()
			}
			revisions[saved.RevisionID] = gift
		}
		if saved.UniqueGiftID != 0 && saved.Unique == nil {
			unique, found := uniques[saved.UniqueGiftID]
			if !found {
				return domain.BotAPIOwnedGiftPage{}, internalErr()
			}
			saved.Unique = &unique
		}
		// The appearance/price is immutable, but upgrade availability is live.
		if saved.UniqueGiftID == 0 {
			// Availability was loaded once for this complete page.
			gift.UpgradeStars, gift.UpgradeTotal, gift.UpgradeIssued = 0, 0, 0
			if a, ok := availability[saved.GiftID]; ok {
				gift.UpgradeStars, gift.UpgradeTotal, gift.UpgradeIssued = a.UpgradeStars, a.SupplyTotal, a.Issued
			}
		}
		item := domain.BotAPIOwnedGift{Saved: saved, Gift: gift, Business: business, Now: now}
		item.CanTransfer = transferGifts && (saved.TransferStars == 0 || transferStars)
		if !saved.NameHidden || business {
			if saved.FromUserID > 0 {
				sender, found, err := r.deps.Users.ByID(ctx, botID, saved.FromUserID)
				if err != nil {
					return domain.BotAPIOwnedGiftPage{}, internalErr()
				}
				if found {
					item.Sender = &sender
				}
			}
			if len(saved.MessageEntities) > 0 {
				item.EntityUsers = make(map[int64]domain.User)
			}
			for _, entity := range saved.MessageEntities {
				if entity.UserID <= 0 {
					continue
				}
				user, found, err := r.deps.Users.ByID(ctx, botID, entity.UserID)
				if err != nil {
					return domain.BotAPIOwnedGiftPage{}, internalErr()
				}
				if found {
					item.EntityUsers[user.ID] = user
				}
			}
		} else {
			item.Saved.Message, item.Saved.MessageEntities = "", nil
			item.Saved.FromUserID = 0
		}
		publisher := gift.ReleasedBy
		if saved.Unique != nil {
			publisher = saved.Unique.ReleasedBy
		}
		if publisher.Type == domain.PeerTypeChannel && publisher.ID > 0 && r.deps.Channels != nil {
			view, err := r.deps.Channels.ResolveChannel(ctx, botID, publisher.ID)
			if err == nil {
				item.Publisher = &view.Channel
			}
		}
		out.Gifts = append(out.Gifts, item)
	}
	return out, nil
}

func (r *Router) botAPIBusinessGift(ctx context.Context, botID int64, connectionID, ownedID, right string) (int64, domain.SavedStarGift, error) {
	owner, err := r.botAPIBusinessOwner(ctx, botID, connectionID, right)
	if err != nil {
		return 0, domain.SavedStarGift{}, err
	}
	msgID, err := strconv.ParseInt(ownedID, 10, 32)
	if err != nil || msgID <= 0 || strconv.FormatInt(msgID, 10) != ownedID || r.deps.Gifts == nil {
		return 0, domain.SavedStarGift{}, errors.New("GIFT_INVALID")
	}
	ref := domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner}, MsgID: int(msgID)}
	saved, found, err := r.deps.Gifts.GetSaved(ctx, ref)
	if err != nil {
		return 0, domain.SavedStarGift{}, internalErr()
	}
	if !found || saved.Owner != ref.Owner || !saved.LifecycleStatus.Live() {
		return 0, domain.SavedStarGift{}, errors.New("GIFT_INVALID")
	}
	return owner, saved, nil
}

func (r *Router) BotAPIConvertGiftToStars(ctx context.Context, botID int64, connectionID, ownedID string) (bool, error) {
	owner, saved, err := r.botAPIBusinessGift(ctx, botID, connectionID, ownedID, "can_convert_gifts_to_stars")
	if err != nil {
		return false, err
	}
	if saved.UniqueGiftID != 0 || saved.ConvertStars <= 0 {
		return false, errors.New("GIFT_INVALID")
	}
	sender, found, err := r.deps.Users.ByID(ctx, owner, saved.FromUserID)
	if err != nil {
		return false, internalErr()
	}
	if !found || sender.Bot {
		return false, errors.New("GIFT_INVALID")
	}
	_, err = r.deps.Gifts.ConvertAggregate(ctx, domain.StarGiftConvertRequest{ActorUserID: owner, Ref: starGiftUpgradeSavedRef(saved), Date: int(r.clock.Now().Unix())})
	if err != nil {
		return false, r.botAPIOwnedGiftError(err)
	}
	r.invalidateStarGiftOwner(saved.Owner)
	return true, nil
}

func (r *Router) BotAPIUpgradeGift(ctx context.Context, botID int64, connectionID, ownedID string, keep bool, stars int64) (bool, error) {
	owner, saved, err := r.botAPIBusinessGift(ctx, botID, connectionID, ownedID, "can_transfer_and_upgrade_gifts")
	if err != nil {
		return false, err
	}
	if stars < 0 {
		return false, errors.New("STAR_COUNT_INVALID")
	}
	prepaid := saved.PrepaidUpgradeStars > 0
	if prepaid && stars != 0 {
		return false, errors.New("STAR_COUNT_INVALID")
	}
	if !prepaid {
		if _, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_transfer_stars"); err != nil {
			return false, err
		}
	}
	preview, err := r.starGiftUpgradePreviewForSaved(ctx, saved)
	if err != nil {
		return false, err
	}
	if !prepaid && stars != preview.UpgradeStars {
		return false, errors.New("STAR_COUNT_INVALID")
	}
	formID := int64(0)
	if !prepaid {
		formID = starGiftUpgradeFormID(owner, saved.ID, stars, keep)
	}
	_, err = r.deps.Gifts.Upgrade(ctx, domain.StarGiftUpgradeRequest{UserID: owner, Ref: starGiftUpgradeSavedRef(saved), KeepOriginalDetails: keep,
		ChargeStars: stars, RequirePrepaid: prepaid, FormID: formID, CommandKey: fmt.Sprintf("botapi-upgrade:%d:%d:%t:%d", botID, saved.ID, keep, stars), Date: int(r.clock.Now().Unix())})
	if err != nil {
		return false, r.botAPIOwnedGiftError(err)
	}
	r.invalidateStarGiftOwner(saved.Owner)
	return true, nil
}

func (r *Router) BotAPITransferGift(ctx context.Context, botID int64, connectionID, ownedID string, newOwnerChatID, stars int64) (bool, error) {
	owner, saved, err := r.botAPIBusinessGift(ctx, botID, connectionID, ownedID, "can_transfer_and_upgrade_gifts")
	if err != nil {
		return false, err
	}
	to, ok := botAPIPeerFromChatID(newOwnerChatID)
	if !ok || to.Type != domain.PeerTypeUser || to.ID == owner {
		return false, errors.New("CHAT_ID_INVALID")
	}
	if saved.UniqueGiftID == 0 {
		return false, errors.New("GIFT_INVALID")
	}
	if stars < 0 || stars != saved.TransferStars {
		return false, errors.New("STAR_COUNT_INVALID")
	}
	if stars > 0 {
		if _, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_transfer_stars"); err != nil {
			return false, err
		}
	}
	var recipient domain.User
	found := false
	// Admission uses activity facts, not the viewer's last-seen projection:
	// hiding an exact timestamp must not make an active recipient ineligible.
	if resolver, ok := r.deps.Users.(privateSendBaseUserResolver); ok {
		users, loadErr := resolver.BaseUsersByIDs(ctx, []int64{to.ID})
		if loadErr != nil {
			return false, internalErr()
		}
		for _, user := range users {
			if user.ID == to.ID {
				recipient, found = user, true
				break
			}
		}
	} else {
		recipient, found, err = r.deps.Users.ByID(ctx, owner, to.ID)
		if err != nil {
			return false, internalErr()
		}
	}
	if !found || recipient.Bot || recipient.Deleted || domain.IsSystemUserID(recipient.ID) {
		return false, userIDInvalidErr()
	}
	if err := r.botAPIBusinessGiftAllowed(ctx, to.ID, true, false, false); err != nil {
		return false, err
	}
	now := int(r.clock.Now().Unix())
	lastActive := recipient.LastSeenAt
	presence := r.userPresenceStatus(to.ID)
	if presence.Kind == domain.UserStatusOnline && presence.Expires > now {
		lastActive = now
	} else if presence.WasOnline > lastActive {
		lastActive = presence.WasOnline
	}
	if lastActive < now-86400 {
		return false, errors.New("RECIPIENT_NOT_ACTIVE")
	}
	unsaved, err := r.starGiftRecipientUnsaved(ctx, owner, to)
	if err != nil {
		return false, err
	}
	_, err = r.deps.Gifts.Transfer(ctx, domain.StarGiftTransferRequest{ActorUserID: owner, Ref: starGiftUpgradeSavedRef(saved), To: to,
		ChargeStars: stars, CommandKey: fmt.Sprintf("botapi-transfer:%d:%d:%d:%d", botID, saved.ID, saved.MsgID, to.ID), Date: now, RecipientUnsaved: unsaved})
	if err != nil {
		return false, r.botAPIOwnedGiftError(err)
	}
	r.invalidateStarGiftOwner(saved.Owner)
	r.invalidateStarGiftOwner(to)
	return true, nil
}

func (r *Router) botAPIOwnedGiftError(err error) error {
	switch {
	case errors.Is(err, domain.ErrStarsInsufficient):
		return starsErr(err)
	case errors.Is(err, domain.ErrStarGiftNotFound), errors.Is(err, domain.ErrStarGiftAlreadyConverted), errors.Is(err, domain.ErrStarGiftAlreadyUpgraded),
		errors.Is(err, domain.ErrStarGiftNotConvertible), errors.Is(err, domain.ErrStarGiftOwnerInvalid), errors.Is(err, domain.ErrStarGiftCollectibleUnavailable),
		errors.Is(err, domain.ErrStarGiftCollectibleSoldOut), errors.Is(err, domain.ErrStarGiftTransferUnavailable), errors.Is(err, domain.ErrStarGiftListed),
		errors.Is(err, domain.ErrStarGiftIdempotencyConflict):
		return errors.New("GIFT_INVALID")
	default:
		return internalErr()
	}
}
