package domain

// ChannelSenderIdentity is the minimal channel data needed to manage a banned
// speaking identity; it does not grant access to the source channel's history.
type ChannelSenderIdentity struct {
	Title      string
	AccessHash int64
	Broadcast  bool
	Megagroup  bool
}

func SenderIdentity(channel Channel) ChannelSenderIdentity {
	return ChannelSenderIdentity{Title: channel.Title, AccessHash: channel.AccessHash, Broadcast: channel.Broadcast, Megagroup: channel.Megagroup}
}

// ChannelSenderBanMember projects a channel identity into the common participant model.
func ChannelSenderBanMember(channelID, senderID, actorID int64, date int) ChannelMember {
	return ChannelMember{ChannelID: channelID, Peer: Peer{Type: PeerTypeChannel, ID: senderID}, InviterUserID: actorID, Role: ChannelRoleMember, Status: ChannelMemberKicked, LeftAt: date, BannedRights: ChannelBannedRights{ViewMessages: true}}
}

// ValidateChannelSenderBan only permits a full, permanent channel-identity ban or removal.
func ValidateChannelSenderBan(req EditChannelBannedRequest) error {
	if req.UserID == 0 || req.ChannelID == 0 || req.Participant.Type != PeerTypeChannel || req.Participant.ID == 0 || req.Participant.ID == req.ChannelID {
		return ErrChannelInvalid
	}
	rights := req.BannedRights
	if rights.ViewMessages && rights.UntilDate != 0 {
		return ErrChannelInvalid
	}
	if !rights.ViewMessages {
		rights.UntilDate = 0
		if rights != (ChannelBannedRights{}) {
			return ErrChannelInvalid
		}
	}
	return nil
}
