package botapi

import (
	"net/http"

	"telesrv/internal/domain"
)

// officialErrorDescriptions 把网关错误码映射成官方 Bot API 的错误说明文本。bot 库
// （grammY、python-telegram-bot 等）按这些固定句子判断失败原因，所以要逐字一致。
//
// 官方说明随方法而变：同是 MESSAGE_ID_INVALID，deleteMessage 报
// "message to delete not found"，editMessageText 报 "message to edit not found"。
// 因此每个方法（或方法组）用 newOfficialErrorDescriptions 声明一张表，不做全局表；表里
// 没有的错误码保持 apiErrorDescription 的结果（TL 错误码本身）。所有表集中在本文件，
// 方法处理函数里不再写 switch 映射，新方法也在这里加表。
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

// deleteMessageErrors 是 deleteMessage 的官方失败说明。
var deleteMessageErrors = newOfficialErrorDescriptions(map[string]string{
	"MESSAGE_ID_INVALID":       "Bad Request: message to delete not found",
	"MESSAGE_DELETE_FORBIDDEN": "Bad Request: message can't be deleted",
})

// deleteMessagesErrors 是 deleteMessages 的官方失败说明：全部删不了时沿用
// deleteMessage 的两句；另加 message_ids 本身的参数错误，这三个码只由网关的
// 参数解析产生。
var deleteMessagesErrors = newOfficialErrorDescriptions(map[string]string{
	"MESSAGE_ID_INVALID":       "Bad Request: message to delete not found",
	"MESSAGE_DELETE_FORBIDDEN": "Bad Request: message can't be deleted",
	"MESSAGE_IDS_EMPTY":        "Bad Request: message identifiers are not specified",
	"MESSAGE_IDS_TOO_MUCH":     "Bad Request: too many message identifiers specified",
	"MESSAGE_IDS_INVALID":      "Bad Request: invalid message identifier specified",
})

// setMyProfilePhotoErrors 是 setMyProfilePhoto 的失败说明。官方服务端把 MTProto 错误
// 转成 "Bad Request: <CODE>"：图片解不开时 Telegram 回 IMAGE_PROCESS_FAILED（本服务的
// 处理器报 PHOTO_INVALID）；文件过大与官方上传超限同一句。
var setMyProfilePhotoErrors = newOfficialErrorDescriptions(map[string]string{
	"PHOTO_INVALID": "Bad Request: IMAGE_PROCESS_FAILED",
	"FILE_TOO_BIG":  "Bad Request: file is too big",
})

// senderChatBanErrors 是两个方法的官方失败说明。官方网关经 TDLib 的
// setChatMemberStatus 执行：没有封禁权限报 restrict/unrestrict 那句；sender_chat_id
// 不是合法的 chat 标识报 invalid chat identifier；目标频道不存在（处理器的
// PEER_ID_INVALID）报 member not found。
var senderChatBanErrors = newOfficialErrorDescriptions(map[string]string{
	"CHAT_ADMIN_REQUIRED":    "Bad Request: not enough rights to restrict/unrestrict chat member",
	"SENDER_CHAT_ID_INVALID": "Bad Request: invalid chat identifier specified",
	"PEER_ID_INVALID":        "Bad Request: member not found",
})

// chatInviteLinkErrors 是 create/edit/revoke/exportChatInviteLink 的官方失败说明。
var chatInviteLinkErrors = newOfficialErrorDescriptions(map[string]string{
	"CHAT_ADMIN_REQUIRED":                "Bad Request: not enough rights to manage chat invite links",
	domain.BotAPIInviteLimitWithApproval: inviteLimitWithApprovalDescription,
})

// inviteLimitWithApprovalDescription 是人数上限与需要审批同时出现时的官方说明：同一请求里
// 两者都传时网关直接拒绝，只传一个时由网关按链接现状判定。
const inviteLimitWithApprovalDescription = "Bad Request: member limit can't be specified for links requiring administrator approval"

// joinRequestErrors 是 approveChatJoinRequest / declineChatJoinRequest 的官方失败说明：
// 官方对这几种 MTProto 错误原样转出，前面加 "Bad Request: "。
var joinRequestErrors = newOfficialErrorDescriptions(map[string]string{
	"HIDE_REQUESTER_MISSING":   "Bad Request: HIDE_REQUESTER_MISSING",
	"USER_ALREADY_PARTICIPANT": "Bad Request: USER_ALREADY_PARTICIPANT",
	"CHAT_ADMIN_REQUIRED":      "Bad Request: CHAT_ADMIN_REQUIRED",
})

// getUserProfilePhotosErrors 是 getUserProfilePhotos 的官方失败说明：用户不存在或机器人
// 看不到（网关的 USER_ID_INVALID）报 user not found。
var getUserProfilePhotosErrors = newOfficialErrorDescriptions(map[string]string{
	"USER_ID_INVALID": "Bad Request: user not found",
})
