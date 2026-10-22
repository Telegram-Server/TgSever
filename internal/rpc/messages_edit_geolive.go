package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 本文件实现 live location 的续报与停止：editMessage + InputMediaGeoLive。
// DrKLO Android 用它周期性上报新坐标；stopped=true 表示停止共享。
// 停止语义：把 Period 改写为「已逝时长」，所有客户端按 message.date + period
// 判定立即过期，服务端不需要后台到点任务。

func (r *Router) onEditMessageLiveLocation(ctx context.Context, req *tg.MessagesEditMessageRequest, media *tg.InputMediaGeoLive) (tg.UpdatesClass, error) {
	if req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	current, found := r.loadLiveLocationMessage(ctx, userID, peer, req.ID)
	if !found {
		return nil, messageIDInvalidErr()
	}
	now := int(r.clock.Now().Unix())
	newMedia, err := editLiveLocationMedia(current.media, current.date, now, media)
	if err != nil {
		return nil, err
	}
	replyMarkup, setReplyMarkup, err := r.editReplyMarkup(ctx, userID, peer, req.ReplyMarkup)
	if err != nil {
		return nil, err
	}

	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return nil, peerIDInvalidErr()
		}
		res, err := r.deps.Channels.EditMessage(ctx, userID, domain.EditChannelMessageRequest{
			UserID:         userID,
			ChannelID:      peer.ID,
			ID:             req.ID,
			Message:        current.body,
			Entities:       current.entities,
			Media:          newMedia,
			EditDate:       now,
			SetReplyMarkup: setReplyMarkup,
			ReplyMarkup:    replyMarkup,
		})
		if err != nil {
			return nil, channelEditErr(err)
		}
		updates := r.channelEditMessageUpdates(ctx, userID, res)
		r.enqueueChannelEditMessageFanout(ctx, userID, res)
		return updates, nil
	}
	if peer.Type != domain.PeerTypeUser || r.deps.Messages == nil {
		return nil, peerIDInvalidErr()
	}
	blocked, err := r.peerBlocksUser(ctx, userID, peer.ID)
	if err != nil {
		return nil, err
	}
	if blocked {
		return nil, messageEditForbiddenErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.EditMessage(ctx, userID, domain.EditMessageRequest{
		OwnerUserID:     userID,
		Peer:            peer,
		ID:              req.ID,
		Message:         current.body,
		Entities:        current.entities,
		Media:           newMedia,
		EditDate:        now,
		SetReplyMarkup:  setReplyMarkup,
		ReplyMarkup:     replyMarkup,
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionID,
	})
	if err != nil {
		return nil, messageEditErr(err)
	}
	return r.privateEditMessageUpdates(ctx, userID, res)
}

// period 是相对首发日期的总有效期；每次最多加一天，总计最多 90 天。
// stopped 独立持久化，保证首发同秒停止也不能在下一秒前被复活。
func editLiveLocationMedia(current *domain.MessageMedia, date, now int, input *tg.InputMediaGeoLive) (*domain.MessageMedia, error) {
	if current == nil || current.Kind != domain.MessageMediaKindGeoLive || current.GeoLive == nil {
		return nil, messageIDInvalidErr()
	}
	if !current.GeoLive.Active(date, now) {
		return nil, messageNotModifiedErr()
	}
	next := *current.GeoLive
	if input.Stopped {
		next.Stopped = true
		next.Period = max(0, now-date)
	} else {
		if _, empty := input.GeoPoint.(*tg.InputGeoPointEmpty); !empty {
			geo, err := domainGeoPointFromInput(input.GeoPoint)
			if err != nil {
				return nil, err
			}
			geo.AccessHash = next.Geo.AccessHash
			next.Geo = *geo
		}
		if heading, ok := input.GetHeading(); ok {
			if heading < 0 || heading > maxLiveLocationHeading {
				return nil, mediaInvalidErr()
			}
			next.Heading = heading
		}
		if period, ok := input.GetPeriod(); ok {
			if period != foreverLiveLocationPeriod && (period < minLiveLocationPeriod || int64(date)+int64(period) > int64(now)+90*86400 || int64(date)+int64(period) <= int64(now) || int64(period) > int64(next.Period)+86400) {
				return nil, mediaInvalidErr()
			}
			next.Period = period
		}
		if radius, ok := input.GetProximityNotificationRadius(); ok {
			if radius < 0 || radius > maxProximityRadiusMeters {
				return nil, mediaInvalidErr()
			}
			next.ProximityNotificationRadius = radius
		}
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: &next}, nil
}

func domainLiveLocationFromInput(input *tg.InputMediaGeoLive) (*domain.MessageMedia, error) {
	if input.Stopped {
		return nil, mediaInvalidErr()
	}
	geo, err := domainGeoPointFromInput(input.GeoPoint)
	if err != nil {
		return nil, err
	}
	live := &domain.MessageGeoLive{Geo: *geo, Period: minLiveLocationPeriod}
	if period, ok := input.GetPeriod(); ok {
		if period != foreverLiveLocationPeriod && (period < minLiveLocationPeriod || period > maxLiveLocationPeriod) {
			return nil, mediaInvalidErr()
		}
		live.Period = period
	}
	if heading, ok := input.GetHeading(); ok {
		if heading < 0 || heading > maxLiveLocationHeading {
			return nil, mediaInvalidErr()
		}
		live.Heading = heading
	}
	if radius, ok := input.GetProximityNotificationRadius(); ok {
		if radius < 0 || radius > maxProximityRadiusMeters {
			return nil, mediaInvalidErr()
		}
		live.ProximityNotificationRadius = radius
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindGeoLive, GeoLive: live}, nil
}

type liveLocationTarget struct {
	media    *domain.MessageMedia
	body     string
	entities []domain.MessageEntity
	date     int
}

// loadLiveLocationMessage 加载目标消息并校验它是一条 live location。
func (r *Router) loadLiveLocationMessage(ctx context.Context, userID int64, peer domain.Peer, msgID int) (liveLocationTarget, bool) {
	switch peer.Type {
	case domain.PeerTypeChannel:
		if r.deps.Channels == nil {
			return liveLocationTarget{}, false
		}
		history, err := r.deps.Channels.GetMessages(ctx, userID, peer.ID, []int{msgID})
		if err != nil {
			return liveLocationTarget{}, false
		}
		for _, msg := range history.Messages {
			if msg.ID == msgID && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindGeoLive && msg.Media.GeoLive != nil {
				return liveLocationTarget{media: msg.Media, body: msg.Body, entities: msg.Entities, date: msg.Date}, true
			}
		}
	case domain.PeerTypeUser:
		if r.deps.Messages == nil {
			return liveLocationTarget{}, false
		}
		list, err := r.deps.Messages.GetMessages(ctx, userID, []int{msgID})
		if err != nil {
			return liveLocationTarget{}, false
		}
		for _, msg := range list.Messages {
			if msg.ID == msgID && msg.Peer == peer && msg.Media != nil && msg.Media.Kind == domain.MessageMediaKindGeoLive && msg.Media.GeoLive != nil {
				return liveLocationTarget{media: msg.Media, body: msg.Body, entities: msg.Entities, date: msg.Date}, true
			}
		}
	}
	return liveLocationTarget{}, false
}
