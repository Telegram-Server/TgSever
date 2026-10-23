package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// BotUpdateStore 是 bot qts 序列（#145）的 postgres 实现：bot_update_states 分配 qts，
// bot_updates 保存记录。成员变动等源事件经 appendBotUpdatesTx 在自己的事务里写入。
type BotUpdateStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

var _ store.BotUpdateStore = (*BotUpdateStore)(nil)

// NewBotUpdateStore 基于 pgx 连接池创建 bot qts 序列 store。
func NewBotUpdateStore(db sqlcgen.DBTX) *BotUpdateStore {
	return &BotUpdateStore{db: db, q: sqlcgen.New(db)}
}

func (s *BotUpdateStore) AppendBotUpdates(ctx context.Context, updates []domain.BotUpdate) ([]domain.BotUpdate, error) {
	if len(updates) == 0 {
		return nil, nil
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return nil, fmt.Errorf("append bot updates: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin append bot updates: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := appendBotUpdatesTx(ctx, tx, updates)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit append bot updates: %w", err)
	}
	return out, nil
}

// appendBotUpdatesTx 在调用方的事务里逐条分配 qts 并写入记录。
func appendBotUpdatesTx(ctx context.Context, tx sqlcgen.DBTX, updates []domain.BotUpdate) ([]domain.BotUpdate, error) {
	q := sqlcgen.New(tx)
	out := make([]domain.BotUpdate, 0, len(updates))
	for _, update := range updates {
		if update.BotUserID == 0 {
			return nil, fmt.Errorf("append bot update: missing bot user id")
		}
		qts, err := q.AllocateBotUpdateQts(ctx, update.BotUserID)
		if err != nil {
			return nil, fmt.Errorf("allocate bot qts: %w", err)
		}
		update.Qts = int(qts)
		payload, err := json.Marshal(update)
		if err != nil {
			return nil, fmt.Errorf("encode bot update: %w", err)
		}
		if err := q.InsertBotUpdate(ctx, sqlcgen.InsertBotUpdateParams{
			BotUserID: update.BotUserID, Qts: qts, Date: int32(update.Date), Payload: payload,
		}); err != nil {
			return nil, fmt.Errorf("insert bot update: %w", err)
		}
		out = append(out, update)
	}
	return out, nil
}

// appendBotOnlyUpdatesTx 只写入主体是 bot 账号的记录：成员变动的事务里调用，bot 身份
// 与成员记录读自同一事务。
func appendBotOnlyUpdatesTx(ctx context.Context, tx sqlcgen.DBTX, updates []domain.BotUpdate) ([]domain.BotUpdate, error) {
	if len(updates) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(updates))
	for _, update := range updates {
		ids = append(ids, update.BotUserID)
	}
	botIDs, err := sqlcgen.New(tx).FilterBotUserIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("filter bot participants: %w", err)
	}
	if len(botIDs) == 0 {
		return nil, nil
	}
	bots := make(map[int64]struct{}, len(botIDs))
	for _, id := range botIDs {
		bots[id] = struct{}{}
	}
	kept := make([]domain.BotUpdate, 0, len(updates))
	for _, update := range updates {
		if _, ok := bots[update.BotUserID]; ok {
			kept = append(kept, update)
		}
	}
	return appendBotUpdatesTx(ctx, tx, kept)
}

func (s *BotUpdateStore) ListBotUpdates(ctx context.Context, botUserID int64, afterQts, limit int) ([]domain.BotUpdate, error) {
	if botUserID == 0 || limit <= 0 {
		return nil, nil
	}
	rows, err := s.q.ListBotUpdates(ctx, sqlcgen.ListBotUpdatesParams{BotUserID: botUserID, Qts: int32(afterQts), Limit: int32(limit)})
	if err != nil {
		return nil, fmt.Errorf("list bot updates: %w", err)
	}
	out := make([]domain.BotUpdate, 0, len(rows))
	for _, row := range rows {
		var update domain.BotUpdate
		if err := json.Unmarshal(row.Payload, &update); err != nil {
			return nil, fmt.Errorf("decode bot update %d/%d: %w", row.BotUserID, row.Qts, err)
		}
		update.BotUserID, update.Qts, update.Date = row.BotUserID, int(row.Qts), int(row.Date)
		out = append(out, update)
	}
	return out, nil
}

func (s *BotUpdateStore) BotUpdatesState(ctx context.Context, botUserID int64) (domain.BotUpdatesState, error) {
	row, err := s.q.GetBotUpdatesState(ctx, botUserID)
	if err != nil {
		return domain.BotUpdatesState{}, fmt.Errorf("get bot updates state: %w", err)
	}
	return domain.BotUpdatesState{Qts: int(row.Qts), MinRetainedQts: int(row.MinRetainedQts)}, nil
}

func (s *BotUpdateStore) DeleteExpiredBotUpdates(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	n, err := s.q.DeleteExpiredBotUpdates(ctx, sqlcgen.DeleteExpiredBotUpdatesParams{Date: int32(before.Unix()), Limit: int32(limit)})
	if err != nil {
		return 0, fmt.Errorf("delete expired bot updates: %w", err)
	}
	return int(n), nil
}
