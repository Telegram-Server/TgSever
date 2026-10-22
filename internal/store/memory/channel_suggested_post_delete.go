package memory

import (
	"fmt"
	"time"

	"telesrv/internal/domain"
)

// Channel deletion is itself a lifecycle transition, under the same aggregate
// lock as approval and dispatch. No escrow may survive its destination channel.
func (s *ChannelStore) finishDeletedChannelSuggestedPostsLocked(channel domain.Channel, date int) error {
	monoID := channel.LinkedMonoforumID
	if channel.Monoforum {
		monoID = channel.ID
	}
	if monoID == 0 {
		return nil
	}
	if date <= 0 {
		date = int(time.Now().Unix())
	}
	pending := false
	for key, approval := range s.suggestedPostApprovals {
		if key.monoforumID == monoID && (approval.state == domain.SuggestedPostStateScheduled || approval.state == domain.SuggestedPostStatePublished) {
			pending = true
			break
		}
	}
	if !pending {
		return nil
	}
	mono, ok := s.channels[monoID]
	if !ok || !mono.Monoforum {
		return domain.ErrSuggestedPostInvalid
	}
	parent, ok := s.channels[mono.LinkedMonoforumID]
	if !ok || !parent.Broadcast || parent.LinkedMonoforumID != mono.ID {
		return domain.ErrSuggestedPostInvalid
	}
	// Validate first: the memory backend cannot roll a transaction back after
	// refunding an earlier entry if a later published receipt is corrupt.
	for key, approval := range s.suggestedPostApprovals {
		if key.monoforumID != mono.ID || approval.state != domain.SuggestedPostStatePublished {
			continue
		}
		if approval.price == nil || approval.settlementDue <= 0 || approval.publishedMessageID <= 0 {
			return domain.ErrSuggestedPostInvalid
		}
		found := false
		for _, message := range s.messages[parent.ID] {
			if message.ID == approval.publishedMessageID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("suggested post deletion invariant: missing published message %d/%d", parent.ID, approval.publishedMessageID)
		}
	}
	for key, approval := range s.suggestedPostApprovals {
		if key.monoforumID != mono.ID || (approval.state != domain.SuggestedPostStateScheduled && approval.state != domain.SuggestedPostStatePublished) {
			continue
		}
		refund := true
		if approval.state == domain.SuggestedPostStatePublished {
			refund = date < approval.settlementDue
			for _, message := range s.messages[parent.ID] {
				if message.ID == approval.publishedMessageID && message.Deleted {
					deleteDate := s.channelMessageDeleteDateLocked(parent.ID, message.ID)
					refund = refund || deleteDate == 0 || deleteDate < approval.settlementDue
					break
				}
			}
		}
		action := domain.ChannelMessageAction{Type: domain.ChannelActionSuggestedPostRefund}
		if refund {
			s.refundSuggestedPostPaymentLocked(approval.savedPeer.ID, approval.price)
			approval.state = domain.SuggestedPostStateRefunded
		} else {
			s.settleSuggestedPostPaymentLocked(parent.ID, approval.price)
			received := cloneSuggestedPostPrice(approval.price)
			received.Amount = received.Amount * paidMessageChannelCommissionPermille / 1000
			action.Type, action.SuggestedPostPrice = domain.ChannelActionSuggestedPostSuccess, received
			approval.state = domain.SuggestedPostStateCompleted
		}
		result := cloneSuggestedPostResult(approval.lastResult)
		result.State, result.Published = approval.state, nil
		if approval.price != nil {
			service, event := s.appendSuggestedPostServiceLocked(mono, parent, approval.actorUserID, approval.savedPeer, key.messageID, date, action)
			result.ServiceMessage, result.ServiceEvent = service, event
		}
		result.Monoforum = cloneChannel(s.channels[mono.ID])
		approval.lastResult = result
		s.suggestedPostApprovals[key] = approval
	}
	return nil
}
