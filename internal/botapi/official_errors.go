package botapi

import (
	"net/http"
)

// officialErrorDescriptions 把网关错误码映射成官方 Bot API 的错误说明文本。bot 库
// （grammY、python-telegram-bot 等）按这些固定句子判断失败原因，所以要逐字一致。
//
// 官方说明随方法而变：同是 MESSAGE_ID_INVALID，deleteMessage 报
// "message to delete not found"，editMessageText 报 "message to edit not found"。
// 因此每个方法（或方法组）在自己的文件里用 newOfficialErrorDescriptions 声明一张表，
// 不做全局表；表里没有的错误码保持 apiErrorDescription 的结果（TL 错误码本身）。
type officialErrorDescriptions map[string]string

// newOfficialErrorDescriptions 建表并把表里的错误码登记进 apiErrorDescription 的
// 白名单：映射的前提是错误码先被识别出来，否则会被折成 BAD_REQUEST。在包级 var
// 里调用即可（早于各文件的 init 运行，登记不会丢）。
func newOfficialErrorDescriptions(descriptions map[string]string) officialErrorDescriptions {
	codes := make([]string, 0, len(descriptions))
	for code := range descriptions {
		codes = append(codes, code)
	}
	registerErrorMarkers(codes...)
	return officialErrorDescriptions(descriptions)
}

// describe 返回 err 的官方说明；未映射的错误码原样返回。
func (d officialErrorDescriptions) describe(err error) string {
	code := apiErrorDescription(err)
	if text, ok := d[code]; ok {
		return text
	}
	return code
}

// writeError 以 400 写出 err 的官方说明。
func (d officialErrorDescriptions) writeError(w http.ResponseWriter, err error) {
	writeAPIError(w, http.StatusBadRequest, d.describe(err))
}

// deleteMessageErrors 是 deleteMessage（以及后续的 deleteMessages）的官方失败说明。
var deleteMessageErrors = newOfficialErrorDescriptions(map[string]string{
	"MESSAGE_ID_INVALID":       "Bad Request: message to delete not found",
	"MESSAGE_DELETE_FORBIDDEN": "Bad Request: message can't be deleted",
})
