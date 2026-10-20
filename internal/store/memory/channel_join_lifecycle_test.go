package memory

import (
	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
	"testing"
)

func TestChannelJoinRequestLifecycle(t *testing.T) {
	s := NewChannelStore()
	storetest.ChannelJoinRequestLifecycle(t, s, [4]int64{1001, 1002, 1003, 1004}, func(channelID int64, invite domain.ChannelInvite, userID int64, date int) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.importers[channelID][userID] = domain.ChannelInviteImporter{ChannelID: channelID, InviteID: invite.InviteID, UserID: userID, Date: date, Requested: true}
		current := s.invites[invite.Hash]
		current.RequestedCount++
		s.invites[invite.Hash] = current
	})
}
