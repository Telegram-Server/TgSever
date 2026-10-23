package domain

import "errors"

const (
	// MaxChatlistInvitePeers bounds one exported shared folder link snapshot.
	MaxChatlistInvitePeers = MaxDialogFolderPeers
	// MaxChatlistInvitesDefault follows the non-premium appConfig limit.
	MaxChatlistInvitesDefault = 3
	// MaxChatlistInvitesPremium follows the premium appConfig limit.
	MaxChatlistInvitesPremium = 20
	// MaxChatlistsJoinedDefault follows the non-premium appConfig limit.
	MaxChatlistsJoinedDefault = 2
	// MaxChatlistsJoinedPremium follows the premium appConfig limit.
	MaxChatlistsJoinedPremium = 20
)

var (
	ErrChatlistInvalid        = errors.New("chatlist invalid")
	ErrChatlistInviteInvalid  = errors.New("chatlist invite invalid")
	ErrChatlistInviteExpired  = errors.New("chatlist invite expired")
	ErrChatlistInviteConflict = errors.New("chatlist invite conflict")
	ErrChatlistInvitesTooMuch = errors.New("chatlist invites too much")
	ErrChatlistsTooMuch       = errors.New("chatlists too much")
	ErrChatlistPeersEmpty     = errors.New("chatlist peers empty")
	ErrChatlistPeersTooMuch   = errors.New("chatlist peers too much")
	ErrChatlistNotShareable   = errors.New("chatlist not shareable")
	ErrChatlistSlugOccupied   = errors.New("chatlist slug occupied")
)

// ChatlistInvite is one exported shared-folder link. Peers is a bounded
// snapshot selected by the owner for this link.
type ChatlistInvite struct {
	ID          int64
	OwnerUserID int64
	FilterID    int
	Slug        string
	Title       string
	Peers       []DialogFolderPeer
	Date        int
	Revoked     bool
	Deleted     bool
}

// ChatlistMembership binds an imported shared-folder link to the viewer's
// local dialog filter.
type ChatlistMembership struct {
	UserID        int64
	LocalFilterID int
	OwnerUserID   int64
	OwnerFilterID int
	Slug          string
	HiddenUpdates bool
	Date          int
}

type ChatlistInvitePreview struct {
	Invite      ChatlistInvite
	OwnerFolder DialogFolder
	LocalFolder *DialogFolder
	Membership  *ChatlistMembership
	Missing     []DialogFolderPeer
	Already     []DialogFolderPeer
}

type ChatlistJoinResult struct {
	Folder         DialogFolder
	Membership     ChatlistMembership
	Date           int
	ChannelResults []CreateChannelResult
	// BotUpdates 是这次操作在各源事件事务里写下的全部 bot qts 记录（加入、提交入群申请、
	// 失败后的回滚退出），调用方在提交后推送一次。操作以错误结束时它也可能非空：入群申请
	// （ErrInviteRequestSent）与已完成的加入都已提交。
	BotUpdates []BotUpdate
}

type ChatlistUpdates struct {
	Membership ChatlistMembership
	Missing    []DialogFolderPeer
}

type ChatlistLeaveResult struct {
	FilterID        int
	ChannelResults  []CreateChannelResult
	RequestedLeaves []DialogFolderPeer
	// BotUpdates 是退出写下的全部 bot qts 记录；操作以错误结束时它也可能非空（已完成的退出
	// 都已提交）。
	BotUpdates []BotUpdate
}
