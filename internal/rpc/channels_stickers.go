package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) applyStickerSetToChannelFull(ctx context.Context, ch domain.Channel, full *tg.ChannelFull) error {
	if ch.StickerSetID == 0 {
		return nil
	}
	if r.deps.Files == nil {
		return internalErr()
	}
	set, _, found, err := r.deps.Files.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: ch.StickerSetID})
	if err != nil {
		return internalErr()
	}
	if found && !set.Deleted {
		full.SetStickerset(tgStickerSet(set))
	}
	return nil
}
