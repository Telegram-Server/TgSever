package memory

import "telesrv/internal/domain"

// AttachMessageReplyStores binds the two real stores. Cross-store sends always
// acquire channel before message locks (after any commerce lock).
func AttachMessageReplyStores(messages *MessageStore, channels *ChannelStore) {
	if messages != nil && channels != nil {
		messages.replyChannels = channels
		channels.replyMessages = messages
	}
}

func replyChannelMessage(message domain.ChannelMessage) domain.Message {
	from := message.From
	if from.Type == "" && message.SenderUserID > 0 {
		from = domain.Peer{Type: domain.PeerTypeUser, ID: message.SenderUserID}
	}
	return domain.Message{ID: message.ID, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: message.ChannelID}, From: from, Date: message.Date, Body: message.Body, Entities: message.Entities, Media: message.Media, NoForwards: message.NoForwards, Deleted: message.Deleted}
}

func (s *ChannelStore) externalChannelReplyLocked(actorID int64, reply *domain.MessageReply) (*domain.MessageReply, error) {
	channel, member, _, err := s.channelForViewerLocked(actorID, reply.Peer.ID)
	if err != nil {
		return nil, err
	}
	source, ok := s.findMessageLocked(reply.Peer.ID, reply.MessageID)
	if !ok || source.Deleted || source.ID <= member.AvailableMinID || !channelMessageVisibleToViewerLocked(channel, member, actorID, source) {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	if channel.Monoforum {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	message := replyChannelMessage(source)
	message.NoForwards = message.NoForwards || channel.NoForwards
	return resolvedExternalReply(reply, message)
}
func resolvedExternalReply(reply *domain.MessageReply, source domain.Message) (*domain.MessageReply, error) {
	if source.NoForwards || source.Media != nil && (source.Media.TTLSeconds > 0 || source.Media.PaidMedia != nil) {
		return nil, domain.ErrChatForwardsRestricted
	}
	if err := domain.ValidateBotAPIReplySource(reply, source); err != nil {
		return nil, err
	}
	out := domain.CloneMessageReply(reply)
	out.AllowMissing = false
	var err error
	out.External, err = domain.NewMessageReplyExternal(source)
	return out, err
}
func (s *ChannelStore) externalPrivateReplyLocked(actorID int64, reply *domain.MessageReply) (*domain.MessageReply, error) {
	if s.replyMessages == nil {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	messages := s.replyMessages
	messages.mu.RLock()
	defer messages.mu.RUnlock()
	for _, source := range messages.m[actorID] {
		if source.ID == reply.MessageID && source.Peer == reply.Peer && !source.Deleted {
			if pair, ok := noForwardsPair(actorID, reply.Peer.ID); ok && messages.privateNoForwards[pair].Enabled() {
				return nil, domain.ErrChatForwardsRestricted
			}
			return resolvedExternalReply(reply, source)
		}
	}
	return nil, domain.ErrReplyMessageIDInvalid
}
