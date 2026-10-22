package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func tgStickerMaskCoords(position *domain.StickerMaskPosition) (tg.MaskCoords, bool) {
	if position == nil {
		return tg.MaskCoords{}, false
	}
	n := 0
	switch position.Point {
	case "forehead":
	case "eyes":
		n = 1
	case "mouth":
		n = 2
	case "chin":
		n = 3
	default:
		return tg.MaskCoords{}, false
	}
	return tg.MaskCoords{N: n, X: position.XShift, Y: position.YShift, Zoom: position.Scale}, true
}

func domainStickerMaskPosition(coords tg.MaskCoords) *domain.StickerMaskPosition {
	points := [...]string{"forehead", "eyes", "mouth", "chin"}
	if coords.N < 0 || coords.N >= len(points) {
		return nil
	}
	return &domain.StickerMaskPosition{Point: points[coords.N], XShift: coords.X, YShift: coords.Y, Scale: coords.Zoom}
}

func inputStickerItemMaskPosition(item tg.InputStickerSetItem) *domain.StickerMaskPosition {
	if coords, ok := item.GetMaskCoords(); ok {
		return domainStickerMaskPosition(coords)
	}
	return nil
}
