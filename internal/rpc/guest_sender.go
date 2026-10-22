package rpc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"

	"telesrv/internal/app/guestauth"
	"telesrv/internal/domain"
)

// sendGuestInlineResult never impersonates the bot as a channel member. The
// single-use query authorizes a reply in exactly the initiating user's chat;
// sendOutgoing retains the user's normal privacy, block and membership gates.
func (r *Router) sendGuestInlineResult(ctx context.Context, botID, queryID int64, result domain.BotInlineResult) (domain.BotInlineMessageID, error) {
	if r.deps.BotGuestQueries == nil || r.deps.Bots == nil {
		return domain.BotInlineMessageID{}, queryIDInvalidErr()
	}
	profile, found, err := r.deps.Bots.BotInfo(ctx, botID)
	if err != nil {
		return domain.BotInlineMessageID{}, internalErr()
	}
	if !found || !profile.GuestChat {
		return domain.BotInlineMessageID{}, botMethodInvalidErr()
	}
	q, found, err := r.deps.BotGuestQueries.GetGuestQuery(ctx, botID, queryID, r.clock.Now())
	if err != nil {
		return domain.BotInlineMessageID{}, internalErr()
	}
	if !found {
		return domain.BotInlineMessageID{}, queryIDInvalidErr()
	}
	if err := r.checkSendRateLimit(ctx, q.CallerUserID, 1); err != nil {
		return domain.BotInlineMessageID{}, err
	}
	if err := r.ensureBotAccess(ctx, botID, q.CallerUserID); err != nil {
		return domain.BotInlineMessageID{}, err
	}
	if err := r.validateGuestSource(ctx, q); err != nil {
		return domain.BotInlineMessageID{}, err
	}
	if r.deps.Contacts != nil {
		for _, userID := range []int64{q.CallerUserID, privateGuestRecipient(q.Peer)} {
			if userID == 0 {
				continue
			}
			blocked, err := r.deps.Contacts.IsBlocked(ctx, userID, botID)
			if err != nil {
				return domain.BotInlineMessageID{}, internalErr()
			}
			if blocked {
				return domain.BotInlineMessageID{}, userIsBlockedErr()
			}
		}
	}
	if q.Peer.Type == domain.PeerTypeUser {
		blocked, err := r.peerBlocksUser(ctx, q.CallerUserID, q.Peer.ID)
		if err != nil {
			return domain.BotInlineMessageID{}, err
		}
		if blocked {
			return domain.BotInlineMessageID{}, userIsBlockedErr()
		}
	}
	media := cloneInlineMedia(result.Media)
	if result.Content != nil {
		r.inlines.registerWebDocumentContext(ctx, r.clock.Now(), *result.Content, q.ExpiresAt.Sub(r.clock.Now()))
	}
	if result.MediaAuto {
		media, err = r.domainInlineExternalContentMedia(ctx, result)
		if err != nil {
			return domain.BotInlineMessageID{}, err
		}
	}
	r.resolveInlineContactMediaForSender(ctx, q.CallerUserID, media)
	var randomBytes [8]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return domain.BotInlineMessageID{}, internalErr()
	}
	randomID := int64(binary.LittleEndian.Uint64(randomBytes[:]))
	if randomID == 0 {
		randomID = 1
	}
	sendCtx, grant, err := guestauth.Consume(ctx, r.deps.BotGuestQueries, botID, queryID, r.clock.Now())
	if err != nil {
		if errors.Is(err, domain.ErrAuthenticatedScopeInvalid) {
			return domain.BotInlineMessageID{}, queryIDInvalidErr()
		}
		return domain.BotInlineMessageID{}, internalErr()
	}
	if grant.CallerUserID != q.CallerUserID || grant.Peer != q.Peer || grant.MessageID != q.MessageID {
		return domain.BotInlineMessageID{}, queryIDInvalidErr()
	}
	updates, _, err := r.sendOutgoing(WithUserID(sendCtx, q.CallerUserID), q.CallerUserID, q.Peer, outgoingSend{
		randomID: randomID, message: result.Message, entities: tgMessageEntities(result.Entities), media: media,
		richMessage: result.RichMessage,
		replyMarkup: result.ReplyMarkup, viaBotID: botID, sendAsReady: true,
		replyTo: &domain.MessageReply{MessageID: q.MessageID}, replyToReady: true,
	})
	if err != nil {
		return domain.BotInlineMessageID{}, err
	}
	id := r.privateInlineMessageIDFromSendUpdates(ctx, botID, q.CallerUserID, updates)
	if q.Peer.Type == domain.PeerTypeChannel {
		id = r.channelInlineMessageIDFromSendUpdates(ctx, botID, q.CallerUserID, updates)
	}
	out, ok := inlineDomainMessageID(id)
	if !ok {
		return domain.BotInlineMessageID{}, internalErr()
	}
	return out, nil
}

func privateGuestRecipient(peer domain.Peer) int64 {
	if peer.Type == domain.PeerTypeUser {
		return peer.ID
	}
	return 0
}

func (r *Router) validateGuestSource(ctx context.Context, q domain.BotGuestQuery) error {
	if q.Peer.Type == domain.PeerTypeUser {
		if err := r.botAPICheckPrivateNoForwards(ctx, q.CallerUserID, q.Peer); err != nil {
			return err
		}
		msg, found, err := r.lookupOwnerMessage(ctx, q.CallerUserID, q.MessageID)
		if err != nil {
			return err
		}
		if !found || msg.Deleted || msg.NoForwards || msg.Peer != q.Peer {
			return messageIDInvalidErr()
		}
		return nil
	}
	if r.deps.Channels == nil {
		return peerIDInvalidErr()
	}
	view, err := r.deps.Channels.GetChannel(ctx, q.CallerUserID, q.Peer.ID)
	if err != nil {
		return channelInvalidErr(err)
	}
	if !view.Channel.Megagroup || view.Channel.Broadcast || view.Channel.NoForwards {
		return channelInvalidErr(domain.ErrChannelWriteForbidden)
	}
	history, err := r.deps.Channels.GetMessages(ctx, q.CallerUserID, q.Peer.ID, []int{q.MessageID})
	if err != nil {
		return channelInvalidErr(err)
	}
	if len(history.Messages) != 1 || history.Messages[0].Deleted || history.Messages[0].NoForwards {
		return messageIDInvalidErr()
	}
	return nil
}
