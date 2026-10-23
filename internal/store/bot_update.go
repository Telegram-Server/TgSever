package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// BotUpdateStore 是 bot qts 序列（#145）的存储：每个 bot 一条连续的 qts 序列。
//
// 源事件（例如成员变动）的 store 在自己的事务里写入记录，保证"源事件提交了 bot 就一定
// 能收到"；AppendBotUpdates 是不依附于其它源事件的独立写入口。
type BotUpdateStore interface {
	// AppendBotUpdates 为每条记录按 BotUserID 原子分配下一个 qts 并写入，返回带 qts 的记录。
	AppendBotUpdates(ctx context.Context, updates []domain.BotUpdate) ([]domain.BotUpdate, error)
	// ListBotUpdates 按 qts 升序返回 qts > afterQts 的至多 limit 条记录。
	ListBotUpdates(ctx context.Context, botUserID int64, afterQts, limit int) ([]domain.BotUpdate, error)
	// BotUpdatesState 返回已分配的最大 qts 与仍保留的最小 qts。
	BotUpdatesState(ctx context.Context, botUserID int64) (domain.BotUpdatesState, error)
	// DeleteExpiredBotUpdates 删除 date 早于 before 的至多 limit 条记录（保留期回收）；
	// 已分配的 qts 不回退。
	DeleteExpiredBotUpdates(ctx context.Context, before time.Time, limit int) (int, error)
}
