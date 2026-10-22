package postgres

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func replyChannelMessage(message domain.ChannelMessage) domain.Message {
	from := message.From
	if from.Type == "" && message.SenderUserID > 0 {
		from = domain.Peer{Type: domain.PeerTypeUser, ID: message.SenderUserID}
	}
	return domain.Message{ID: message.ID, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: message.ChannelID}, From: from, Date: message.Date, Body: message.Body, Entities: message.Entities, Media: message.Media, NoForwards: message.NoForwards, Deleted: message.Deleted}
}

func loadPrivateReplyMessage(row sqlcgen.GetMessageBoxForReplyRow, peer domain.Peer) (domain.Message, error) {
	entities, err := decodeMessageEntities(row.EntitiesJson)
	if err != nil {
		return domain.Message{}, err
	}
	media, err := decodeMessageMedia(row.MediaJson)
	if err != nil {
		return domain.Message{}, err
	}
	return domain.Message{ID: int(row.BoxID), Peer: peer, From: domain.Peer{Type: domain.PeerTypeUser, ID: row.FromUserID}, Date: int(row.MessageDate), Body: row.Body, Entities: entities, Media: media, NoForwards: row.Noforwards}, nil
}

func resolveExternalReply(ctx context.Context, db sqlcgen.DBTX, actorID int64, reply *domain.MessageReply) (*domain.MessageReply, error) {
	var source domain.Message
	q := sqlcgen.New(db)
	if reply.Peer.Type == domain.PeerTypeUser {
		row, err := q.GetMessageBoxForReply(ctx, sqlcgen.GetMessageBoxForReplyParams{OwnerUserID: actorID, PeerType: string(reply.Peer.Type), PeerID: reply.Peer.ID, BoxID: int32(reply.MessageID)})
		if err != nil {
			return nil, err
		}
		source, err = loadPrivateReplyMessage(row, reply.Peer)
		if err != nil {
			return nil, err
		}
		protected, err := q.IsBotReplyPrivateProtected(ctx, sqlcgen.IsBotReplyPrivateProtectedParams{UserLowID: min(actorID, reply.Peer.ID), UserHighID: max(actorID, reply.Peer.ID)})
		if err != nil {
			return nil, err
		}
		source.NoForwards = source.NoForwards || protected
	} else if reply.Peer.Type == domain.PeerTypeChannel {
		channels := NewChannelStore(db)
		channel, member, _, err := channels.getChannelForViewer(ctx, db, actorID, reply.Peer.ID)
		if err != nil {
			return nil, err
		}
		if _, err := q.LockBotReplyChannelSource(ctx, sqlcgen.LockBotReplyChannelSourceParams{ChannelID: reply.Peer.ID, ID: int32(reply.MessageID)}); err != nil {
			return nil, err
		}
		message, err := channels.getChannelMessage(ctx, db, reply.Peer.ID, reply.MessageID)
		if err != nil {
			return nil, err
		}
		if channel.Monoforum {
			return nil, domain.ErrReplyMessageIDInvalid
		}
		if message.Deleted || message.ID <= member.AvailableMinID || !channelMessageVisibleToViewer(channel, member, actorID, message) {
			return nil, domain.ErrReplyMessageIDInvalid
		}
		source = replyChannelMessage(message)
		source.NoForwards = source.NoForwards || channel.NoForwards
	} else {
		return nil, domain.ErrReplyMessageIDInvalid
	}
	if source.NoForwards || source.Media != nil && (source.Media.TTLSeconds > 0 || source.Media.PaidMedia != nil) {
		return nil, domain.ErrChatForwardsRestricted
	}
	if err := domain.ValidateBotAPIReplySource(reply, source); err != nil {
		return nil, err
	}
	result := domain.CloneMessageReply(reply)
	result.AllowMissing = false
	var err error
	result.External, err = domain.NewMessageReplyExternal(source)
	return result, err
}
