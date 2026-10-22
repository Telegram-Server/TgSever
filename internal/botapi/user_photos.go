package botapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

// UserPhotosGateway 提供 getUserProfilePhotos：以机器人为查看者读取用户头像相册。
// 可选接口：网关不实现时该方法回 501 METHOD_NOT_FOUND。
type UserPhotosGateway interface {
	// BotAPIGetUserProfilePhotos 返回相册的一页（offset 起最多 limit 张，新的在前）
	// 与相册总数；相册已按对方的头像隐私对机器人裁剪，与客户端看到的一致。
	// 用户不存在或机器人看不到时返回 USER_ID_INVALID。
	BotAPIGetUserProfilePhotos(ctx context.Context, botID, userID int64, offset, limit int) ([]domain.Photo, int, error)
}

const (
	// 官方 Bot API 的 getUserProfilePhotos 参数错误描述（网关解析 user_id 失败时直接写出；
	// 网关错误码的说明见 official_errors.go 的 getUserProfilePhotosErrors）。
	userPhotosInvalidUserIDDescription = "Bad Request: invalid user_id specified"

	// maxUserProfilePhotosLimit 是官方 limit 的上限，也是缺省值。
	maxUserProfilePhotosLimit = 100
)

func init() {
	registerMethods(map[string]methodFunc{
		"getUserProfilePhotos": (*handler).getUserProfilePhotos,
	})
}

func (h *handler) getUserProfilePhotos(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(UserPhotosGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(values["user_id"]), 10, 64)
	if err != nil || userID <= 0 {
		writeAPIError(w, http.StatusBadRequest, userPhotosInvalidUserIDDescription)
		return
	}
	// 与官方实现一致：offset 下限 0，limit 夹到 1–100，缺省分别为 0 与 100。
	offset := max(apiInt(values["offset"], 0), 0)
	limit := min(max(apiInt(values["limit"], maxUserProfilePhotosLimit), 1), maxUserProfilePhotosLimit)
	photos, total, err := gateway.BotAPIGetUserProfilePhotos(r.Context(), botID, userID, offset, limit)
	if err != nil {
		getUserProfilePhotosErrors.writeError(w, err)
		return
	}
	writeAPIOK(w, apiUserProfilePhotosResult(photos, total))
}

// apiUserProfilePhotosResult 投影官方 UserProfilePhotos。
func apiUserProfilePhotosResult(photos []domain.Photo, total int) map[string]any {
	out := make([][]map[string]any, 0, len(photos))
	for _, photo := range photos {
		out = append(out, apiProfilePhotoSizes(photo))
	}
	if total < len(out) {
		total = len(out)
	}
	return map[string]any{"total_count": total, "photos": out}
}

// apiProfilePhotoSizes 与消息照片同样生成 file_id（可直接 getFile / sendPhoto），
// 但去掉动态头像的视频尺寸：PhotoSize 只描述静态图。
func apiProfilePhotoSizes(photo domain.Photo) []map[string]any {
	sizes := make([]domain.PhotoSize, 0, len(photo.Sizes))
	for _, size := range photo.Sizes {
		if size.Kind != domain.PhotoSizeKindVideo {
			sizes = append(sizes, size)
		}
	}
	photo.Sizes = sizes
	return apiPhotoSizes(photo)
}
