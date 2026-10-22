package rpc

import (
	"context"
	"telesrv/internal/domain"
)

// Approval, publication, escrow settlement and refund all reach this shared
// fanout, including the scheduled lifecycle dispatcher.
func (r *Router) enqueueBotAPISuggestedPostUpdates(ctx context.Context, result domain.ToggleSuggestedPostApprovalResult) {
	if r == nil || r.deps.BotAPIUpdates == nil {
		return
	}
	recipients := make(map[int64]struct{}, len(result.Recipients)+1)
	for _, id := range result.Recipients {
		recipients[id] = struct{}{}
	}
	if result.SavedPeer.Type == domain.PeerTypeUser {
		recipients[result.SavedPeer.ID] = struct{}{}
	}
	for botID := range recipients {
		bot, err := r.botAPIKnownBot(ctx, botID)
		if err != nil || !bot {
			continue
		}
		if result.OriginalEvent.Pts > 0 && result.OriginalMessage.ID > 0 {
			r.enqueueBotAPIMessageSnapshot(ctx, botID, domain.BotAPIUpdateEditedMessage, botAPIMessageFromChannel(botID, result.OriginalMessage), []domain.Channel{result.Monoforum, result.Parent})
		}
		if result.ServiceEvent.Pts > 0 && result.ServiceMessage.ID > 0 {
			r.enqueueBotAPIMessageSnapshot(ctx, botID, domain.BotAPIUpdateMessage, botAPIMessageFromChannel(botID, result.ServiceMessage), []domain.Channel{result.Monoforum, result.Parent})
		}
	}
}
