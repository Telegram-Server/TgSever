package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// maxBotAPIStaticProfilePhotoBytes 是静态头像文件的上限，与 MTProto 照片上传的 10MB 一致；
// Bot API 的 multipart 上限（25MB）更宽，超出部分在这里拒绝。
const maxBotAPIStaticProfilePhotoBytes = 10 << 20

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

// BotAPISetMyProfilePhoto 是 Bot API setMyProfilePhoto 的网关：把网关解析好的
// InputProfilePhoto 按头像规则生成 photo，再走 photos.uploadProfilePhoto（不带 bot 参数）
// 的落地路径设为机器人的当前头像；给机器人自己和所有者的 updateUser 都继承该路径。
func (r *Router) BotAPISetMyProfilePhoto(ctx context.Context, botID int64, photo domain.BotAPIProfilePhoto) (bool, error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return false, errors.New("BOT_INVALID")
	}
	if r.deps.Files == nil {
		return false, notImplementedErr()
	}
	ctx = WithUserID(ctx, botID)
	created, err := r.createBotAPIProfilePhoto(ctx, photo)
	if err != nil {
		return false, err
	}
	if _, err := r.setUploadedProfilePhoto(ctx, botID, domain.User{}, domain.ProfilePhotoKindProfile, created); err != nil {
		return false, err
	}
	return true, nil
}

// createBotAPIProfilePhoto 按 InputProfilePhoto 的种类把上传字节存成头像 photo。
// 新种类（animated）在这里加一个分支，调用文件服务对应的字节入口。
func (r *Router) createBotAPIProfilePhoto(ctx context.Context, photo domain.BotAPIProfilePhoto) (domain.Photo, error) {
	switch photo.Kind {
	case domain.BotAPIProfilePhotoStatic:
		if len(photo.Data) > maxBotAPIStaticProfilePhotoBytes {
			return domain.Photo{}, tgerr.New(400, "FILE_TOO_BIG")
		}
		created, err := r.deps.Files.CreateAvatarFromBytes(ctx, photo.Data)
		if err != nil {
			return domain.Photo{}, photoUploadErr(err)
		}
		return created, nil
	default:
		return domain.Photo{}, photoInvalidErr()
	}
}
