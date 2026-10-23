package store

import (
	"context"

	"telesrv/internal/domain"
)

// ChannelBannedSenderChatStore 持久化"某个频道 peer 在某个群/频道里被封禁"（#137）。
// memory 与 postgres 的 ChannelStore 都实现它；保持为独立接口，免得给只实现
// ChannelStore 的轻量测试替身加负担。
type ChannelBannedSenderChatStore interface {
	// EditChannelBannedSenderChat 封禁（BannedRights.ViewMessages）或解封（全空）一个频道 peer。
	// 执行者必须是群主或有 ban_users 的管理员，否则 ErrChannelAdminRequired；其他权限组合
	// 返回 ErrChannelBannedRightsInvalid；目标就是本群，或封禁时目标频道不存在/已删除，返回
	// ErrChannelSenderChatInvalid（解封只要求目标频道存在过，已删除的也可以解封）。真正发生变化时在同一事务里写管理日志
	// （participant_kick / participant_unkick），重复操作返回 Changed=false 且不写日志。
	EditChannelBannedSenderChat(ctx context.Context, req domain.EditChannelBannedSenderChatRequest) (domain.EditChannelBannedSenderChatResult, error)
	// GetChannelBannedSenderChat 不做查看者鉴权，直接返回封禁记录；供发言拦截等内部路径使用。
	GetChannelBannedSenderChat(ctx context.Context, channelID, senderChatID int64) (domain.ChannelBannedSenderChat, bool, error)
	// ListChannelBannedSenderChats 返回一页封禁记录（按 SenderChatID 升序）和总数。查看者必须是
	// 管理员，否则返回空列表（与 kicked 筛选对非管理员的语义一致）。query 非空时按频道标题或
	// 用户名（不区分大小写）子串过滤。limit<=0 时只返回 Count。
	// 已删除的频道不计入、不列出。
	ListChannelBannedSenderChats(ctx context.Context, viewerUserID, channelID int64, query string, offset, limit int) (domain.ChannelBannedSenderChatList, error)
	// ListChannelBannedSenderChatIDs 按与 ListChannelBannedSenderChats 相同的过滤和排序，只返回
	// 前 limit 条封禁记录的频道 id：不计数、不读频道行、不做查看者鉴权。供 kicked 筛选的
	// 缓存 hash 判断页里可能出现哪些频道。
	ListChannelBannedSenderChatIDs(ctx context.Context, channelID int64, query string, limit int) ([]int64, error)
	// GetSenderChatsByIDs 一次读出一批频道 peer 的基础行（按 id 升序，去重），不存在的跳过，
	// 不做查看者鉴权；供管理日志、单个成员查询等投影补齐频道 peer。已删除的频道也返回，
	// Deleted=true，只保证 ID、AccessHash、Title、Broadcast、Megagroup，供投影成
	// channelForbidden。
	GetSenderChatsByIDs(ctx context.Context, ids []int64) ([]domain.Channel, error)
}
