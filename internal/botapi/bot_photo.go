package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

// BotProfilePhotoGateway 管理机器人自己的头像，对应 MTProto photos.updateProfilePhoto /
// photos.uploadProfilePhoto（不带 bot 参数，即机器人改自己）。头像变化后给所有者推
// updateUser 由网关背后的处理器路径负责。
// 可选接口：网关不实现时这组方法回 501 METHOD_NOT_FOUND。
type BotProfilePhotoGateway interface {
	BotAPIRemoveMyProfilePhoto(ctx context.Context, botID int64) (bool, error)
	// BotAPISetMyProfilePhoto 按头像规则保存上传的文件并设为机器人的当前头像。
	BotAPISetMyProfilePhoto(ctx context.Context, botID int64, photo domain.BotAPIProfilePhoto) (bool, error)
}

func init() {
	registerMethods(map[string]methodFunc{
		"removeMyProfilePhoto": removeMyProfilePhoto,
		"setMyProfilePhoto":    setMyProfilePhoto,
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

// setMyProfilePhotoErrors 是 setMyProfilePhoto 的失败说明。官方服务端把 MTProto 错误
// 转成 "Bad Request: <CODE>"：图片解不开时 Telegram 回 IMAGE_PROCESS_FAILED（本服务的
// 处理器报 PHOTO_INVALID）；文件过大与官方上传超限同一句。
var setMyProfilePhotoErrors = newOfficialErrorDescriptions(map[string]string{
	"PHOTO_INVALID": "Bad Request: IMAGE_PROCESS_FAILED",
	"FILE_TOO_BIG":  "Bad Request: file is too big",
})

// setMyProfilePhoto 把 multipart 上传的文件设为机器人自己的头像；photo 是 InputProfilePhoto。
func setMyProfilePhoto(h *handler, w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(BotProfilePhotoGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, files, err := requestValuesWithFiles(r)
	if err != nil {
		setMyProfilePhotoErrors.writeError(w, err)
		return
	}
	photo, description := parseInputProfilePhoto(values["photo"], files)
	if description != "" {
		writeAPIError(w, http.StatusBadRequest, description)
		return
	}
	if _, err := gateway.BotAPISetMyProfilePhoto(r.Context(), botID, photo); err != nil {
		setMyProfilePhotoErrors.writeError(w, err)
		return
	}
	writeAPIOK(w, true)
}

const (
	inputProfilePhotoRequiredDescription = `Bad Request: parameter "photo" is required`
	inputProfilePhotoParseDescription    = "Bad Request: can't parse InputProfilePhoto JSON object"
	inputProfilePhotoTypeDescription     = "Bad Request: invalid InputProfilePhoto type specified"
	inputProfilePhotoUploadDescription   = "Bad Request: profile photo must be uploaded as a new file using attach://"
)

// inputProfilePhotoParser 解析某一种 InputProfilePhoto 的专属字段；失败时返回说明文本。
type inputProfilePhotoParser func(object map[string]json.RawMessage, files map[string]uploadedFile) (domain.BotAPIProfilePhoto, string)

// inputProfilePhotoParsers 按 type 分派。新种类在这里登记一个解析函数，文件部分复用
// profilePhotoUpload。
var inputProfilePhotoParsers = map[domain.BotAPIProfilePhotoKind]inputProfilePhotoParser{
	domain.BotAPIProfilePhotoStatic:   parseInputProfilePhotoStatic,
	domain.BotAPIProfilePhotoAnimated: parseInputProfilePhotoAnimated,
}

// parseInputProfilePhoto 解析 setMyProfilePhoto 的 photo 参数（JSON 序列化的 InputProfilePhoto）。
// 返回的说明非空表示参数错误。
func parseInputProfilePhoto(raw string, files map[string]uploadedFile) (domain.BotAPIProfilePhoto, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return domain.BotAPIProfilePhoto{}, inputProfilePhotoRequiredDescription
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &object); err != nil || object == nil {
		return domain.BotAPIProfilePhoto{}, inputProfilePhotoParseDescription
	}
	var kind string
	if rawKind, found := object["type"]; !found || json.Unmarshal(rawKind, &kind) != nil {
		return domain.BotAPIProfilePhoto{}, inputProfilePhotoTypeDescription
	}
	parse, found := inputProfilePhotoParsers[domain.BotAPIProfilePhotoKind(kind)]
	if !found {
		return domain.BotAPIProfilePhoto{}, inputProfilePhotoTypeDescription
	}
	return parse(object, files)
}

// parseInputProfilePhotoStatic 解析 InputProfilePhotoStatic：photo 字段指向上传的图片。
func parseInputProfilePhotoStatic(object map[string]json.RawMessage, files map[string]uploadedFile) (domain.BotAPIProfilePhoto, string) {
	file, err := profilePhotoUpload(object, "photo", files)
	if err != nil {
		return domain.BotAPIProfilePhoto{}, inputProfilePhotoUploadDescription
	}
	return domain.BotAPIProfilePhoto{
		Kind:     domain.BotAPIProfilePhotoStatic,
		FileName: file.Name,
		MimeType: file.MimeType,
		Data:     file.Bytes,
	}, ""
}

const (
	mainFrameTimestampNumberDescription = `Bad Request: field "main_frame_timestamp" must be a valid Number`
	mainFrameTimestampWrongDescription  = "Bad Request: wrong main frame timestamp specified"
)

// parseInputProfilePhotoAnimated 解析 InputProfilePhotoAnimated：animation 字段指向上传的
// MPEG4 视频；可选的 main_frame_timestamp（秒，缺省 0）选定作为静态头像的那一帧，
// 与官方一样数字和数字字符串都接受，负数拒绝。
func parseInputProfilePhotoAnimated(object map[string]json.RawMessage, files map[string]uploadedFile) (domain.BotAPIProfilePhoto, string) {
	file, err := profilePhotoUpload(object, "animation", files)
	if err != nil {
		return domain.BotAPIProfilePhoto{}, inputProfilePhotoUploadDescription
	}
	var mainFrame float64
	if raw, found := object["main_frame_timestamp"]; found && string(raw) != "null" {
		var parsed bool
		mainFrame, parsed = jsonNumber(raw)
		if !parsed {
			return domain.BotAPIProfilePhoto{}, mainFrameTimestampNumberDescription
		}
		if mainFrame < 0 {
			return domain.BotAPIProfilePhoto{}, mainFrameTimestampWrongDescription
		}
	}
	return domain.BotAPIProfilePhoto{
		Kind:               domain.BotAPIProfilePhotoAnimated,
		FileName:           file.Name,
		MimeType:           file.MimeType,
		Data:               file.Bytes,
		MainFrameTimestamp: mainFrame,
	}, ""
}

// jsonNumber 读取 JSON 数字或数字字符串，只接受有限值。
func jsonNumber(raw json.RawMessage) (float64, bool) {
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, false
	}
	return number, true
}

var errProfilePhotoNotUploaded = errors.New("profile photo is not an uploaded file")

// profilePhotoUpload 取 InputProfilePhoto 里 field 字段引用的上传文件。官方规定头像不能复用：
// 只接受 "attach://<name>" 指向本次 multipart 请求里的文件，file_id 与 URL 都拒绝。
func profilePhotoUpload(object map[string]json.RawMessage, field string, files map[string]uploadedFile) (uploadedFile, error) {
	var ref string
	if raw, found := object[field]; !found || json.Unmarshal(raw, &ref) != nil {
		return uploadedFile{}, errProfilePhotoNotUploaded
	}
	name, isAttach := strings.CutPrefix(strings.TrimSpace(ref), "attach://")
	if !isAttach || name == "" {
		return uploadedFile{}, errProfilePhotoNotUploaded
	}
	file, found := files[name]
	if !found || len(file.Bytes) == 0 {
		return uploadedFile{}, errProfilePhotoNotUploaded
	}
	return file, nil
}
