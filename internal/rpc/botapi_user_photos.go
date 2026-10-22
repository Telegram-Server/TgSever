package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// BotAPIGetUserProfilePhotos 实现 Bot API getUserProfilePhotos：以机器人为查看者读
// 用户头像相册的一页。与 photos.getUserPhotos 共用 profilePhotoAlbumPage，墓碑与
// 头像隐私（PrivacyKeyProfilePhoto）的裁剪和客户端完全一致。
func (r *Router) BotAPIGetUserProfilePhotos(ctx context.Context, botID, userID int64, offset, limit int) ([]domain.Photo, int, error) {
	if userID <= 0 {
		return nil, 0, errors.New("USER_ID_INVALID")
	}
	if r == nil || botID == 0 || r.deps.Users == nil {
		return nil, 0, errors.New("BOT_INVALID")
	}
	target, found, err := r.userFromInput(ctx, botID, &tg.InputUser{UserID: userID})
	if err != nil {
		return nil, 0, err
	}
	if !found {
		return nil, 0, errors.New("USER_ID_INVALID")
	}
	if r.deps.Files == nil {
		return nil, 0, nil
	}
	return r.profilePhotoAlbumPage(ctx, botID, target, max(offset, 0), limit, 0)
}
