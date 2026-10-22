package domain

// GuestChatPeer decodes the explicit durable guest provenance column. Ordinary
// inline messages and forwarded/saved copies have zero provenance by default;
// From and ViaBotID are never used to infer guest authorization.
func GuestChatPeer(userID int64) Peer {
	if userID <= 0 {
		return Peer{}
	}
	return Peer{Type: PeerTypeUser, ID: userID}
}

// GuestSenderUserID changes only the displayed author, never the mailbox owner
// or channel member whose permissions and slowmode apply to the command.
func GuestSenderUserID(callerID, botID int64) int64 {
	if botID != 0 {
		return botID
	}
	return callerID
}

func GuestChatViaFromID(callerID, botID int64) int64 {
	if botID == 0 {
		return 0
	}
	return callerID
}
