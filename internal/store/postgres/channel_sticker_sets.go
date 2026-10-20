package postgres

import (
	"context"
	"fmt"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) SetStickers(ctx context.Context, userID, channelID, stickerSetID int64) (domain.Channel, error) {
	if userID == 0 || channelID == 0 || stickerSetID < 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.Channel{}, fmt.Errorf("set channel stickers: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.Channel{}, fmt.Errorf("begin set channel stickers: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ch, member, err := s.getChannelForMember(ctx, tx, userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if !ch.Megagroup || ch.Broadcast {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	if stickerSetID != 0 && ch.ParticipantsCount < domain.MinChannelStickerSetParticipants {
		return domain.Channel{}, domain.ErrChannelParticipantsTooFew
	}
	if err := sqlcgen.New(tx).SetChannelStickerSet(ctx, sqlcgen.SetChannelStickerSetParams{ID: channelID, StickerSetID: stickerSetID}); err != nil {
		return domain.Channel{}, fmt.Errorf("set channel stickers: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Channel{}, fmt.Errorf("commit set channel stickers: %w", err)
	}
	if s.rowCache != nil {
		s.rowCache.delete(channelID)
	}
	ch.StickerSetID = stickerSetID
	return ch, nil
}
