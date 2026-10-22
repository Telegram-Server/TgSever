package domain

// BotAPIChatInviteLink 是 Bot API ChatInviteLink 对象的协议无关形态：邀请记录本身、
// 对外的完整链接（含公开域名）与创建者资料。邀请链接方法的返回值和 chat_join_request
// 更新里的 invite_link 都由它投影，所以放在 domain 而不是网关包里。
type BotAPIChatInviteLink struct {
	Invite  ChannelInvite
	Link    string
	Creator User
}

// BotAPIChatInviteLinkOptions 是 createChatInviteLink / editChatInviteLink 的可选字段。
// nil 表示调用方没有传这个参数：edit 只改传入的字段，create 把未传的字段视为默认值。
type BotAPIChatInviteLinkOptions struct {
	Name               *string
	ExpireDate         *int
	MemberLimit        *int
	CreatesJoinRequest *bool
}

// Bot API ChatInviteLink 参数边界（官方文档）。
const (
	BotAPIInviteLinkNameMaxLength  = MaxChannelInviteTitleLength
	BotAPIInviteLinkMemberLimitMin = 1
	BotAPIInviteLinkMemberLimitMax = 99999
)
