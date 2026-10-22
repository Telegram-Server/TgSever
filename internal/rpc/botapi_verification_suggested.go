package rpc

import (
	"context"
	"errors"
	"unicode/utf8"

	"telesrv/internal/domain"
)

type verificationChannelReader interface {
	GetChannelByID(context.Context, int64) (domain.Channel, error)
}

// Verification is an organization-owned mark, never the platform's verified flag.
func (r *Router) BotAPISetVerification(ctx context.Context, botID, chatID int64, enabled bool, description string) error {
	if _, err := r.callerBotID(WithUserID(ctx, botID)); err != nil {
		return err
	}
	if r.deps.BotVerifications == nil {
		return botVerifierForbiddenErr()
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return peerIDInvalidErr()
	}
	if utf8.RuneCountInString(description) > 70 {
		return errors.New("CUSTOM_DESCRIPTION_INVALID")
	}
	if peer.Type == domain.PeerTypeChannel {
		reader, ok := r.deps.Channels.(verificationChannelReader)
		if !ok {
			return peerIDInvalidErr()
		}
		channel, err := reader.GetChannelByID(ctx, peer.ID)
		if err != nil || channel.Monoforum {
			return peerIDInvalidErr()
		}
	}
	if !enabled {
		mark, err := r.deps.BotVerifications.PeerVerification(ctx, peer)
		if err == nil && mark.VerifierBotID != botID {
			return botVerifierForbiddenErr()
		}
		if err != nil && !errors.Is(err, domain.ErrCustomVerificationNotFound) {
			return setCustomVerificationErr(err)
		}
	}
	_, err := r.deps.BotVerifications.SetCustomVerification(ctx, domain.SetCustomVerificationRequest{
		VerifierBotID: botID, CallerUserID: botID, Peer: peer, Enabled: enabled, CustomDescription: description,
	})
	if err != nil {
		return setCustomVerificationErr(err)
	}
	return nil
}

func (r *Router) BotAPIResolveVerificationChatUsername(ctx context.Context, botID int64, username string) (int64, error) {
	if _, err := r.callerBotID(WithUserID(ctx, botID)); err != nil {
		return 0, err
	}
	if users, ok := r.deps.Users.(UserIdentityService); ok {
		user, found, err := users.ResolveUsername(ctx, botID, username)
		if err != nil {
			return 0, usernameErr(err)
		}
		if found {
			if !user.Bot {
				return 0, peerIDInvalidErr()
			}
			return user.ID, nil
		}
	}
	return r.BotAPIResolveChatUsername(ctx, botID, username)
}

// The channel aggregate reserves, settles and refunds Stars/TON atomically. This
// edge only supplies the bot identity and emits the aggregate's committed events.
func (r *Router) BotAPIToggleSuggestedPost(ctx context.Context, botID, chatID int64, messageID int, reject bool, comment string, sendDate int) error {
	if _, err := r.callerBotID(WithUserID(ctx, botID)); err != nil {
		return err
	}
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok || peer.Type != domain.PeerTypeChannel || r.deps.Channels == nil {
		return peerIDInvalidErr()
	}
	if messageID <= 0 || messageID > domain.MaxMessageBoxID {
		return messageIDInvalidErr()
	}
	if utf8.RuneCountInString(comment) > 128 || (!reject && comment != "") {
		return errors.New("COMMENT_INVALID")
	}
	now := int(r.clock.Now().Unix())
	if sendDate < 0 || sendDate > now+domain.MaxSuggestedPostScheduleDelay || (reject && sendDate != 0) {
		return errors.New("SEND_DATE_INVALID")
	}
	reader, ok := r.deps.Channels.(verificationChannelReader)
	if !ok {
		return peerIDInvalidErr()
	}
	mono, err := reader.GetChannelByID(ctx, peer.ID)
	if err != nil || mono.Deleted || !mono.Monoforum || mono.LinkedMonoforumID == 0 {
		return peerIDInvalidErr()
	}
	manager, err := r.deps.Channels.GetParticipant(ctx, botID, mono.LinkedMonoforumID, botID)
	if err != nil || !manager.CanManageDirectMessages() || (!reject && !manager.CanPostChannelMessages()) {
		return tgerr400("CHAT_ADMIN_REQUIRED")
	}
	service, ok := r.deps.Channels.(suggestedPostApprovalService)
	if !ok {
		return notImplementedErr()
	}
	result, err := service.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{
		UserID: botID, MonoforumID: peer.ID, MessageID: messageID, Reject: reject, RejectComment: comment, ScheduleDate: sendDate, Date: now,
	})
	if err != nil {
		return suggestedPostApprovalErr(err)
	}
	if !result.Duplicate {
		r.enqueueSuggestedPostApprovalFanout(ctx, botID, result)
	}
	if result.State == domain.SuggestedPostStateBalanceLow {
		return errors.New("SUGGESTED_POST_BALANCE_TOO_LOW")
	}
	return nil
}
