package domain

import "errors"

// 频道导入：把外部来源（官方 Telegram 的频道）的历史克隆进一个本地广播频道，之后持续镜像。
// 克隆程序在仓库之外，经 admin API 调用；这里是它与存储层之间的协议无关类型。

// ChannelImportState 是导入任务所处的阶段。
type ChannelImportState string

const (
	// ChannelImportImporting 历史回填中：批量写入不产生逐条 update 事件，也不推送。
	ChannelImportImporting ChannelImportState = "importing"
	// ChannelImportMirroring 回填完成：来源的新帖、编辑、删除按正常事件下发给成员。
	ChannelImportMirroring ChannelImportState = "mirroring"
	// ChannelImportDetached 不再镜像：频道是一个普通频道，任务只读。
	ChannelImportDetached ChannelImportState = "detached"
)

// MaxChannelImportBatch 是一次批量写入的消息数上限。
const MaxChannelImportBatch = 500

var (
	// ErrChannelImportNotFound 导入任务不存在。
	ErrChannelImportNotFound = errors.New("channel import not found")
	// ErrChannelImportState 任务所处的阶段不允许这个操作（例如对 detached 的任务写入）。
	ErrChannelImportState = errors.New("channel import state does not allow this operation")
	// ErrChannelImportOrder 批内来源 id 不是严格递增，或一条从未写入过的消息排在已写入的最大
	// 来源 id 之前。历史只能按来源 id 从小到大回填。
	ErrChannelImportOrder = errors.New("channel import messages out of order")
	// ErrChannelImportTarget 目标频道不能作为导入目标：不是广播频道，或除了“频道已创建”之外
	// 已经有别的消息。
	ErrChannelImportTarget = errors.New("channel is not a valid import target")
	// ErrChannelImportExists 目标频道已经有导入任务。
	ErrChannelImportExists = errors.New("channel already has an import")
)

// ChannelImport 是一个导入任务。
type ChannelImport struct {
	ID              int64
	ChannelID       int64
	State           ChannelImportState
	SourceChannelID int64
	SourceUsername  string
	// SenderUserID 是导入帖子挂在其名下的本地账号。广播频道的帖子显示为频道发出，
	// 这个账号客户端看不到，只是 channel_messages.sender_user_id 的外键目标。
	SenderUserID int64
	// MaxSourceID 是已写入的最大来源消息 id，克隆程序据此断点续传。
	MaxSourceID int
	CreatedAt   int
	FinalizedAt int
}

// ImportedChannelMessage 是一条来源消息。SourceID 是它在来源频道里的 id；本地 id 由存储层
// 决定：来源 id 还没被本地占用时沿用，否则分配下一个本地 id 并记入映射。
type ImportedChannelMessage struct {
	SourceID int
	// Date 是来源的发送时间，原样写入；EditDate 同理，0 表示未编辑。
	Date     int
	EditDate int
	Body     string
	Entities []MessageEntity
	Media    *MessageMedia
	// GroupedID 相册分组 id，同一相册的消息共享非零值。
	GroupedID int64
	// ReplyToSourceID 是被回复消息的来源 id；映射表里查得到就换成本地 id，查不到原样保留
	// （目标在来源已被删除，客户端显示为已删除的消息）。
	ReplyToSourceID int
	Forward         *MessageForward
	PostAuthor      string
	// Views / Forwards 是来源的计数快照，只增不减地写入。
	Views       int
	Forwards    int
	Silent      bool
	NoForwards  bool
	Pinned      bool
	ReplyMarkup *MessageReplyMarkup
	// Action 非空时这是一条服务消息。导入的服务消息只是历史记录：改标题、换头像、置顶
	// 都不会真的作用到频道上。
	Action *ChannelMessageAction
}

// ImportChannelMessagesRequest 向一个导入任务写入一批消息。Messages 的 SourceID 必须严格递增。
type ImportChannelMessagesRequest struct {
	ImportID int64
	Messages []ImportedChannelMessage
	// Now 是事件时间（服务器当前时间）；消息本身保留来源日期。0 时由存储层取当前时间。
	Now int
}

// ImportChannelMessagesResult 是一批写入的结果。
type ImportChannelMessagesResult struct {
	Import  ChannelImport
	Channel Channel
	// Written 新写入的条数，Edited 内容有变化而被更新的条数，Unchanged 内容相同而跳过的条数。
	Written   int
	Edited    int
	Unchanged int
	// LocalIDs 是本批每条消息的来源 id → 本地 id。
	LocalIDs map[int]int
	// Events 只在 mirroring 阶段非空：每条新帖或编辑对应一个事件，供调用方实时下发。
	Events []ChannelUpdateEvent
}

// ChannelImportMediaKind 区分导入媒体是照片还是文档。
type ChannelImportMediaKind string

const (
	ChannelImportMediaPhoto    ChannelImportMediaKind = "photo"
	ChannelImportMediaDocument ChannelImportMediaKind = "document"
)

// ChannelImportMedia 是一个来源文件对应的本地媒体。
type ChannelImportMedia struct {
	Kind    ChannelImportMediaKind
	MediaID int64
}

// ChannelImportStats 是任务的进度计数。
type ChannelImportStats struct {
	Messages int
	Media    int
}

// RewriteChannelCreateRequest 把目标频道的“频道已创建”服务消息与频道创建日期改成来源的。
type RewriteChannelCreateRequest struct {
	ChannelID int64
	Date      int
	Title     string
	// Drop 表示来源的这条服务消息已不存在：本地这条标记为已删除，留成空号。
	Drop bool
}
