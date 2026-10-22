package botapi

import (
	"encoding/json"
	"strings"

	"telesrv/internal/domain"
)

// botAPIChatPermissions 是官方 ChatPermissions 对象。官方语义里缺省字段就是
// false，所以用普通 bool 而不是指针。
type botAPIChatPermissions struct {
	CanSendMessages       bool `json:"can_send_messages"`
	CanSendAudios         bool `json:"can_send_audios"`
	CanSendDocuments      bool `json:"can_send_documents"`
	CanSendPhotos         bool `json:"can_send_photos"`
	CanSendVideos         bool `json:"can_send_videos"`
	CanSendVideoNotes     bool `json:"can_send_video_notes"`
	CanSendVoiceNotes     bool `json:"can_send_voice_notes"`
	CanSendPolls          bool `json:"can_send_polls"`
	CanSendOtherMessages  bool `json:"can_send_other_messages"`
	CanAddWebPagePreviews bool `json:"can_add_web_page_previews"`
	CanChangeInfo         bool `json:"can_change_info"`
	CanInviteUsers        bool `json:"can_invite_users"`
	CanPinMessages        bool `json:"can_pin_messages"`
	CanManageTopics       bool `json:"can_manage_topics"`
}

func parseChatPermissions(raw string) (botAPIChatPermissions, error) {
	var p botAPIChatPermissions
	err := json.Unmarshal([]byte(raw), &p)
	return p, err
}

// chatPermissionsToBanned 把"允许"翻成本服的"禁止"位。independent 对应
// use_independent_chat_permissions：为 false 时官方把任一媒体/投票/其他消息/
// 链接预览权限视为同时允许纯文本消息。
func chatPermissionsToBanned(p botAPIChatPermissions, independent bool) domain.ChannelBannedRights {
	if !independent && (p.CanSendAudios || p.CanSendDocuments || p.CanSendPhotos || p.CanSendVideos ||
		p.CanSendVideoNotes || p.CanSendVoiceNotes || p.CanSendPolls || p.CanSendOtherMessages || p.CanAddWebPagePreviews) {
		p.CanSendMessages = true
	}
	b := domain.ChannelBannedRights{
		SendPlain:       !p.CanSendMessages,
		SendAudios:      !p.CanSendAudios,
		SendDocs:        !p.CanSendDocuments,
		SendPhotos:      !p.CanSendPhotos,
		SendVideos:      !p.CanSendVideos,
		SendRoundvideos: !p.CanSendVideoNotes,
		SendVoices:      !p.CanSendVoiceNotes,
		SendPolls:       !p.CanSendPolls,
		SendStickers:    !p.CanSendOtherMessages,
		SendGifs:        !p.CanSendOtherMessages,
		SendGames:       !p.CanSendOtherMessages,
		SendInline:      !p.CanSendOtherMessages,
		EmbedLinks:      !p.CanAddWebPagePreviews,
		ChangeInfo:      !p.CanChangeInfo,
		InviteUsers:     !p.CanInviteUsers,
		PinMessages:     !p.CanPinMessages,
		ManageTopics:    !p.CanManageTopics,
	}
	// send_media / send_messages 是总开关：只有其下每一项都被禁时才置位，
	// 否则老客户端会把仍被允许的细项也一并当成禁止。
	b.SendMedia = b.SendAudios && b.SendDocs && b.SendPhotos && b.SendVideos && b.SendRoundvideos && b.SendVoices
	b.SendMessages = b.SendPlain && b.SendMedia && b.SendPolls && b.SendStickers && b.EmbedLinks
	return b
}

// apiChatPermissions 把禁止位投影回官方 ChatPermissions；总开关被禁时其下所有
// 发送类权限一并为 false。
func apiChatPermissions(b domain.ChannelBannedRights) map[string]bool {
	media := b.SendMessages || b.SendMedia
	send := !b.SendMessages
	return map[string]bool{
		"can_send_messages":         send && !b.SendPlain,
		"can_send_audios":           !media && !b.SendAudios,
		"can_send_documents":        !media && !b.SendDocs,
		"can_send_photos":           !media && !b.SendPhotos,
		"can_send_videos":           !media && !b.SendVideos,
		"can_send_video_notes":      !media && !b.SendRoundvideos,
		"can_send_voice_notes":      !media && !b.SendVoices,
		"can_send_polls":            send && !b.SendPolls,
		"can_send_other_messages":   send && !(b.SendStickers || b.SendGifs || b.SendGames || b.SendInline),
		"can_add_web_page_previews": send && !b.EmbedLinks,
		"can_change_info":           !b.ChangeInfo,
		"can_invite_users":          !b.InviteUsers,
		"can_pin_messages":          !b.PinMessages,
		"can_manage_topics":         !b.ManageTopics,
	}
}

// adminRightFields 是官方 ChatAdministratorRights 字段与本服管理员位的一一对应。
var adminRightFields = []struct {
	name string
	get  func(*domain.ChannelAdminRights) *bool
}{
	{"is_anonymous", func(r *domain.ChannelAdminRights) *bool { return &r.Anonymous }},
	{"can_manage_chat", func(r *domain.ChannelAdminRights) *bool { return &r.ManageChat }},
	{"can_delete_messages", func(r *domain.ChannelAdminRights) *bool { return &r.DeleteMessages }},
	{"can_manage_video_chats", func(r *domain.ChannelAdminRights) *bool { return &r.ManageCall }},
	{"can_restrict_members", func(r *domain.ChannelAdminRights) *bool { return &r.BanUsers }},
	{"can_promote_members", func(r *domain.ChannelAdminRights) *bool { return &r.AddAdmins }},
	{"can_change_info", func(r *domain.ChannelAdminRights) *bool { return &r.ChangeInfo }},
	{"can_invite_users", func(r *domain.ChannelAdminRights) *bool { return &r.InviteUsers }},
	{"can_post_stories", func(r *domain.ChannelAdminRights) *bool { return &r.PostStories }},
	{"can_edit_stories", func(r *domain.ChannelAdminRights) *bool { return &r.EditStories }},
	{"can_delete_stories", func(r *domain.ChannelAdminRights) *bool { return &r.DeleteStories }},
	{"can_post_messages", func(r *domain.ChannelAdminRights) *bool { return &r.PostMessages }},
	{"can_edit_messages", func(r *domain.ChannelAdminRights) *bool { return &r.EditMessages }},
	{"can_pin_messages", func(r *domain.ChannelAdminRights) *bool { return &r.PinMessages }},
	{"can_manage_topics", func(r *domain.ChannelAdminRights) *bool { return &r.ManageTopics }},
	{"can_manage_tags", func(r *domain.ChannelAdminRights) *bool { return &r.ManageRanks }},
	{"can_manage_direct_messages", func(r *domain.ChannelAdminRights) *bool { return &r.ManageDirectMessages }},
}

// adminRightsFromParams 读取 promoteChatMember 等方法的顶层布尔参数；缺省为 false。
func adminRightsFromParams(values map[string]string) domain.ChannelAdminRights {
	var rights domain.ChannelAdminRights
	for _, field := range adminRightFields {
		*field.get(&rights) = strings.EqualFold(strings.TrimSpace(values[field.name]), "true")
	}
	return rights
}

// apiAdministratorRights 投影官方 ChatAdministratorRights 的全部布尔字段。
func apiAdministratorRights(rights domain.ChannelAdminRights) map[string]bool {
	out := make(map[string]bool, len(adminRightFields))
	for _, field := range adminRightFields {
		out[field.name] = *field.get(&rights)
	}
	return out
}

// normalizeUntilDate 实现官方 until_date 规则：距现在不足 30 秒或超过 366 天（含
// 已过去的时间）都视为永久，用 0 表示。
func normalizeUntilDate(raw, now int) int {
	if raw <= 0 {
		return 0
	}
	delta := raw - now
	if delta < 30 || delta > 366*86400 {
		return 0
	}
	return raw
}
