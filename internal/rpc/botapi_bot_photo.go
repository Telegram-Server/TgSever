package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
)

// BotAPIRemoveMyProfilePhoto 是 Bot API removeMyProfilePhoto 的网关：以机器人身份调用
// photos.updateProfilePhoto 并传 inputPhotoEmpty（不带 bot 参数，即机器人删自己的主头像）。
// 删除、缓存失效以及给机器人自己和所有者的 updateUser 推送都继承处理器路径。
//
// 机器人没有头像时返回 true：官方 Bot API 服务端把该方法实现为
// td_api::deleteProfilePhoto(0)，TDLib 对 bot 先重载 UserFull，主头像为空就直接成功，
// 不会发出请求；处理器在没有当前头像时同样成功。
func (r *Router) BotAPIRemoveMyProfilePhoto(ctx context.Context, botID int64) (bool, error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return false, errors.New("BOT_INVALID")
	}
	if _, err := r.onPhotosUpdateProfilePhoto(WithUserID(ctx, botID), &tg.PhotosUpdateProfilePhotoRequest{
		ID: &tg.InputPhotoEmpty{},
	}); err != nil {
		return false, err
	}
	return true, nil
}
