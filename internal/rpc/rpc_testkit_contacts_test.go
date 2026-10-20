package rpc

import "context"

// recipientBlocksContacts models one blocked pair and otherwise delegates to an
// optional real contacts service. It is shared by photo and message regressions.
type recipientBlocksContacts struct {
	ContactsService
	blocker, blocked int64
}

func (c recipientBlocksContacts) IsBlocked(ctx context.Context, userID, peerUserID int64) (bool, error) {
	if userID == c.blocker && peerUserID == c.blocked {
		return true, nil
	}
	if c.ContactsService == nil {
		return false, nil
	}
	return c.ContactsService.IsBlocked(ctx, userID, peerUserID)
}
