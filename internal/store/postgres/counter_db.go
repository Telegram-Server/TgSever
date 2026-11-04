package postgres

import (
	"context"

	"telesrv/internal/store/postgres/sqlcgen"
)

// 持有事务时绝不再从连接池借第二条连接。
//
// id 分配器（Redis 分配器在 key 缺失时、测试分配器每次、pg* 回退分配器总是）要回
// PostgreSQL 查当前最大 id。它们在事务里被调用时，若这条查询走连接池，并发数一旦达到
// 连接池大小，每个事务都占着一条连接等第二条，全部卡死；连接池默认按 CPU 数取大小，
// 所以只在核少的机器上发作。分配器接口不带 DB（Redis 实现与它无关），调用方把当前
// 事务放进 ctx，本包的回源查询用它而不是自己的连接池。

type counterDBKey struct{}

// withCounterDB 返回一个 ctx：其上的 id 分配回源查询在 db（调用方事务）上执行。
// db 为 nil 时原样返回 ctx。这个 ctx 只用于这一次分配调用，不要传给事务提交之后的操作。
func withCounterDB(ctx context.Context, db sqlcgen.DBTX) context.Context {
	if db == nil {
		return ctx
	}
	return context.WithValue(ctx, counterDBKey{}, db)
}

// counterDB 返回 ctx 携带的调用方事务，没有时返回 fallback（回源自己的连接池）。
func counterDB(ctx context.Context, fallback sqlcgen.DBTX) sqlcgen.DBTX {
	if db, ok := ctx.Value(counterDBKey{}).(sqlcgen.DBTX); ok && db != nil {
		return db
	}
	return fallback
}

// nextChannelMessageID 在事务 tx 内分配频道消息 id；回源查询走 tx。
func (s *ChannelStore) nextChannelMessageID(ctx context.Context, tx sqlcgen.DBTX, channelID int64) (int, error) {
	return s.msgIDs.NextChannelMessageID(withCounterDB(ctx, tx), channelID)
}

// nextBoxID 在事务 tx 内分配私聊 box id；回源查询走 tx。
func (s *MessageStore) nextBoxID(ctx context.Context, tx sqlcgen.DBTX, userID int64) (int, error) {
	return s.boxIDs.NextBoxID(withCounterDB(ctx, tx), userID)
}
