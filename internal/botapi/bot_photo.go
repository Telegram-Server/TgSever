package botapi

import (
	"context"
	"net/http"
)

// BotProfilePhotoGateway 管理机器人自己的头像，对应 MTProto photos.updateProfilePhoto /
// photos.uploadProfilePhoto（不带 bot 参数，即机器人改自己）。头像变化后给所有者推
// updateUser 由网关背后的处理器路径负责。
// 可选接口：网关不实现时这组方法回 501 METHOD_NOT_FOUND。
type BotProfilePhotoGateway interface {
	BotAPIRemoveMyProfilePhoto(ctx context.Context, botID int64) (bool, error)
}

func init() {
	registerMethods(map[string]methodFunc{
		"removeMyProfilePhoto": removeMyProfilePhoto,
	})
}

// removeMyProfilePhoto 删除机器人的当前头像；没有参数，没有头像时与官方一样返回 True。
func removeMyProfilePhoto(h *handler, w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(BotProfilePhotoGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	if _, err := gateway.BotAPIRemoveMyProfilePhoto(r.Context(), botID); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}
