package rpc

import (
	"context"
	"errors"
	"math"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func (r *Router) reserveAlbumGroup(ctx context.Context, userID int64, peer domain.Peer, items []domain.AlbumGroupReservationItem) (int64, error) {
	reservations, ok := r.deps.Messages.(AlbumGroupService)
	if !ok {
		r.log.Error("messages.sendMultiMedia album reservation capability missing",
			append(r.contextLogFields(ctx), zap.Int64("user_id", userID), zap.String("peer_type", string(peer.Type)), zap.Int64("peer_id", peer.ID))...)
		return 0, internalErr()
	}
	groupedID, err := reservations.ReserveAlbumGroup(ctx, userID, domain.AlbumGroupReservationRequest{
		SenderUserID:      userID,
		Peer:              peer,
		Items:             items,
		ProposedGroupedID: randomPositiveInt64(),
	})
	if errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		return 0, randomIDDuplicateErr()
	}
	if err != nil {
		r.log.Error("messages.sendMultiMedia album reservation failed",
			append(r.contextLogFields(ctx), zap.Error(err), zap.Int64("user_id", userID), zap.String("peer_type", string(peer.Type)), zap.Int64("peer_id", peer.ID), zap.Int("items", len(items)))...)
		return 0, internalErr()
	}
	if groupedID == 0 {
		r.log.Error("messages.sendMultiMedia album reservation returned zero grouped_id",
			append(r.contextLogFields(ctx), zap.Int64("user_id", userID), zap.String("peer_type", string(peer.Type)), zap.Int64("peer_id", peer.ID))...)
		return 0, internalErr()
	}
	return groupedID, nil
}

// randomPositiveInt64 取密码学随机正 int64，用于新相册的 grouped_id。官方服务端
// 分配的 grouped_id 都是正数，TDLib 把负数留给本地待发送相册
// (generate_new_media_album_id)，telegram-bot-api 又把它原样输出为
// media_group_id，所以这里清掉符号位，结果为 0 时重取。
func randomPositiveInt64() int64 {
	for {
		if v := randomNonZeroInt64() & math.MaxInt64; v != 0 {
			return v
		}
	}
}
