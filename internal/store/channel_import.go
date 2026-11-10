package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"telesrv/internal/domain"
)

// ChannelImportStore 持久化频道导入任务，并把来源消息批量写进目标频道。
//
// 它与 ChannelStore 是同一个后端对象上的另一个接口：导入要和普通发送共用消息表、pts 与
// 消息 id 分配器，但不走发送事务（没有讨论组副本、管理日志、提及和逐条事件）。
type ChannelImportStore interface {
	// CreateChannelImport 为 channelID 建一个 importing 状态的任务。频道已有任务时返回
	// domain.ErrChannelImportExists。
	CreateChannelImport(ctx context.Context, imp domain.ChannelImport) (domain.ChannelImport, error)
	GetChannelImport(ctx context.Context, importID int64) (domain.ChannelImport, error)
	// FindChannelImportBySource 返回来源频道当前在用（非 detached）的任务。
	FindChannelImportBySource(ctx context.Context, sourceChannelID int64) (domain.ChannelImport, bool, error)
	SetChannelImportState(ctx context.Context, importID int64, state domain.ChannelImportState) (domain.ChannelImport, error)
	ChannelImportStats(ctx context.Context, importID int64) (domain.ChannelImportStats, error)

	// ImportChannelMessages 在一个事务里写入一批来源消息。
	//
	//   - 来源 id 已在映射表里且内容相同：跳过（计数只增不减地刷新）；内容不同：按编辑更新。
	//   - 新消息：来源 id 还没被本地占用就沿用，否则分配下一个本地 id；两种情况都记入映射。
	//   - importing 阶段整批只推进一次 pts，不写逐条事件；mirroring 阶段每条写一个事件，
	//     事件时间是 req.Now，消息日期仍是来源日期。
	ImportChannelMessages(ctx context.Context, req domain.ImportChannelMessagesRequest) (domain.ImportChannelMessagesResult, error)
	// ResolveChannelImportMessageIDs 把来源消息 id 换成本地 id；没有映射的 id 不出现在结果里。
	ResolveChannelImportMessageIDs(ctx context.Context, importID int64, sourceIDs []int) (map[int]int, error)

	// RewriteChannelCreateMessage 把目标频道的“频道已创建”服务消息和频道创建日期改成来源的。
	RewriteChannelCreateMessage(ctx context.Context, req domain.RewriteChannelCreateRequest) error
	// SetChannelImportMeta 镜像来源的标题和简介（nil 表示不改）：不写服务消息、不推进 pts。
	SetChannelImportMeta(ctx context.Context, channelID int64, title, about *string) (domain.Channel, error)
	// MarkChannelImportRead 把频道现有成员的已读位置推到最新一条消息。
	MarkChannelImportRead(ctx context.Context, channelID int64) error
	// ReserveChannelMessageIDs 让频道之后分配的消息 id 都大于 through（来源末尾被删掉的号段）。
	ReserveChannelMessageIDs(ctx context.Context, channelID int64, through int) error

	GetChannelImportMedia(ctx context.Context, importID int64, sourceKey string) (domain.ChannelImportMedia, bool, error)
	// PutChannelImportMedia 记下来源文件对应的本地媒体；同一个 sourceKey 已有映射时返回已有的。
	PutChannelImportMedia(ctx context.Context, importID int64, sourceKey string, media domain.ChannelImportMedia) (domain.ChannelImportMedia, error)
}

// ChannelMessageIDClaimer 是 ChannelMessageIDAllocator 的可选扩展：把计数器原子地推进到一个
// 指定的 id。导入沿用来源消息 id 时用它占号，之后的普通发送从这个 id 之后继续分配。
type ChannelMessageIDClaimer interface {
	// ClaimChannelMessageID 在 id 大于频道已分配的最大 id 时把计数器推进到 id 并返回 true；
	// 否则不改计数器并返回 false（这个 id 已经被占用或跳过）。
	ClaimChannelMessageID(ctx context.Context, channelID int64, id int) (bool, error)
}

// ChannelImportContentHash 是一条来源消息内容的摘要，决定重发时是跳过还是按编辑更新。
// 浏览数和转发数不参与：它们每次同步都在变，单独只增不减地刷新。
func ChannelImportContentHash(msg domain.ImportedChannelMessage) ([]byte, error) {
	msg.Views = 0
	msg.Forwards = 0
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("hash imported channel message %d: %w", msg.SourceID, err)
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

// ValidateChannelImportBatch 检查一批来源消息的形状：非空、不超过上限、来源 id 为正且严格
// 递增、每条消息有正文、媒体或服务动作之一。
func ValidateChannelImportBatch(messages []domain.ImportedChannelMessage) error {
	if len(messages) == 0 || len(messages) > domain.MaxChannelImportBatch {
		return domain.ErrChannelInvalid
	}
	previous := 0
	for _, msg := range messages {
		if msg.SourceID <= previous {
			return domain.ErrChannelImportOrder
		}
		previous = msg.SourceID
		if msg.Date <= 0 {
			return domain.ErrChannelInvalid
		}
		if msg.Body == "" && msg.Media.IsZero() && msg.Action == nil {
			return domain.ErrChannelInvalid
		}
	}
	return nil
}
