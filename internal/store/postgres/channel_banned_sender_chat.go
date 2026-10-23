package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

var _ store.ChannelBannedSenderChatStore = (*ChannelStore)(nil)

// EditChannelBannedSenderChat 封禁或解封一个频道 peer（见 store.ChannelBannedSenderChatStore）。
// 封禁用 INSERT … ON CONFLICT DO NOTHING、解封用 DELETE … RETURNING，是否真正变化由返回行
// 判定，并发的重复操作也只会有一个写管理日志。
func (s *ChannelStore) EditChannelBannedSenderChat(ctx context.Context, req domain.EditChannelBannedSenderChatRequest) (domain.EditChannelBannedSenderChatResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.SenderChatID == 0 {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelInvalid
	}
	ban, err := domain.SenderChatBanFromRights(req.BannedRights)
	if err != nil {
		return domain.EditChannelBannedSenderChatResult{}, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.EditChannelBannedSenderChatResult{}, fmt.Errorf("edit channel banned sender chat: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.EditChannelBannedSenderChatResult{}, fmt.Errorf("begin edit channel banned sender chat: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, actor, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelBannedSenderChatResult{}, err
	}
	if !canBanChannelUsers(actor) {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelAdminRequired
	}
	if req.SenderChatID == req.ChannelID {
		return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelSenderChatInvalid
	}
	q := sqlcgen.New(tx)
	// 封禁要求目标频道存在且未删除；解封只要求它存在过，频道删除后封禁记录仍可解除。
	if ban {
		if _, err := getChannelByID(ctx, tx, req.SenderChatID); err != nil {
			if errors.Is(err, domain.ErrChannelInvalid) {
				return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelSenderChatInvalid
			}
			return domain.EditChannelBannedSenderChatResult{}, err
		}
	} else {
		existed, err := q.SenderChatChannelEverExisted(ctx, req.SenderChatID)
		if err != nil {
			return domain.EditChannelBannedSenderChatResult{}, fmt.Errorf("check sender chat channel: %w", err)
		}
		if !existed {
			return domain.EditChannelBannedSenderChatResult{}, domain.ErrChannelSenderChatInvalid
		}
	}
	res := domain.EditChannelBannedSenderChatResult{Channel: channel, Banned: ban}
	var row sqlcgen.ChannelBannedSenderChat
	if ban {
		row, err = q.InsertChannelBannedSenderChat(ctx, sqlcgen.InsertChannelBannedSenderChatParams{
			ChannelID: req.ChannelID, SenderChatID: req.SenderChatID, BannedByUserID: req.UserID, BannedAt: int32(req.Date),
		})
	} else {
		row, err = q.DeleteChannelBannedSenderChat(ctx, sqlcgen.DeleteChannelBannedSenderChatParams{ChannelID: req.ChannelID, SenderChatID: req.SenderChatID})
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 重复操作：封禁时返回现有记录，解封时没有记录。
		if ban {
			existing, err := q.GetChannelBannedSenderChat(ctx, sqlcgen.GetChannelBannedSenderChatParams{ChannelID: req.ChannelID, SenderChatID: req.SenderChatID})
			if err != nil {
				return domain.EditChannelBannedSenderChatResult{}, fmt.Errorf("get channel banned sender chat: %w", err)
			}
			res.Ban = channelBannedSenderChatFromRow(existing)
		}
	case err != nil:
		return domain.EditChannelBannedSenderChatResult{}, fmt.Errorf("edit channel banned sender chat: %w", err)
	default:
		res.Changed = true
		res.Ban = channelBannedSenderChatFromRow(row)
		if err := s.insertChannelAdminLogTx(ctx, tx, domain.SenderChatBanAdminLogEvent(req, res.Ban, ban, req.Date)); err != nil {
			return domain.EditChannelBannedSenderChatResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EditChannelBannedSenderChatResult{}, fmt.Errorf("commit edit channel banned sender chat: %w", err)
	}
	committed = true
	return res, nil
}

// GetChannelBannedSenderChat 返回封禁记录，不做查看者鉴权。
func (s *ChannelStore) GetChannelBannedSenderChat(ctx context.Context, channelID, senderChatID int64) (domain.ChannelBannedSenderChat, bool, error) {
	row, err := sqlcgen.New(s.db).GetChannelBannedSenderChat(ctx, sqlcgen.GetChannelBannedSenderChatParams{ChannelID: channelID, SenderChatID: senderChatID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelBannedSenderChat{}, false, nil
	}
	if err != nil {
		return domain.ChannelBannedSenderChat{}, false, fmt.Errorf("get channel banned sender chat: %w", err)
	}
	return channelBannedSenderChatFromRow(row), true, nil
}

// ListChannelBannedSenderChats 返回一页封禁记录；非管理员查看者得到空列表。
func (s *ChannelStore) ListChannelBannedSenderChats(ctx context.Context, viewerUserID, channelID int64, query string, offset, limit int) (domain.ChannelBannedSenderChatList, error) {
	_, viewer, err := s.getChannelForMember(ctx, s.db, viewerUserID, channelID)
	if errors.Is(err, domain.ErrChannelInvalid) {
		return domain.ChannelBannedSenderChatList{}, err
	}
	if err != nil || !isChannelAdmin(viewer) {
		return domain.ChannelBannedSenderChatList{}, nil
	}
	pattern := ""
	if query = strings.ToLower(strings.TrimSpace(query)); query != "" {
		pattern = adminLogLikePattern(query)
	}
	q := sqlcgen.New(s.db)
	count, err := q.CountChannelBannedSenderChats(ctx, sqlcgen.CountChannelBannedSenderChatsParams{ChannelID: channelID, Pattern: pattern})
	if err != nil {
		return domain.ChannelBannedSenderChatList{}, fmt.Errorf("count channel banned sender chats: %w", err)
	}
	out := domain.ChannelBannedSenderChatList{Count: int(count)}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || offset >= out.Count {
		return out, nil
	}
	rows, err := q.ListChannelBannedSenderChats(ctx, sqlcgen.ListChannelBannedSenderChatsParams{
		ChannelID: channelID, Pattern: pattern, PageOffset: int32(offset), PageLimit: int32(limit),
	})
	if err != nil {
		return domain.ChannelBannedSenderChatList{}, fmt.Errorf("list channel banned sender chats: %w", err)
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		out.Bans = append(out.Bans, channelBannedSenderChatFromRow(row))
		ids = append(ids, row.SenderChatID)
	}
	channels, err := s.channelsByIDs(ctx, s.db, ids)
	if err != nil {
		return domain.ChannelBannedSenderChatList{}, err
	}
	for _, id := range ids {
		if channel, ok := channels[id]; ok {
			out.SenderChats = append(out.SenderChats, channel)
		}
	}
	return out, nil
}

// GetSenderChatsByIDs 一次读出一批频道 peer 的基础行（见 store.ChannelBannedSenderChatStore）。
// 现存频道走频道行缓存；缓存只装现存频道，剩下的 id 再查一次已删除的频道。
func (s *ChannelStore) GetSenderChatsByIDs(ctx context.Context, ids []int64) ([]domain.Channel, error) {
	ids = uniqueNonZeroInt64s(ids...)
	if len(ids) == 0 {
		return nil, nil
	}
	channels, err := s.channelsByIDs(ctx, s.db, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Channel, 0, len(ids))
	missing := make([]int64, 0, len(ids)-len(channels))
	for _, id := range ids {
		if channel, ok := channels[id]; ok {
			out = append(out, channel)
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		rows, err := sqlcgen.New(s.db).ListDeletedSenderChatsByIDs(ctx, missing)
		if err != nil {
			return nil, fmt.Errorf("list deleted sender chats: %w", err)
		}
		for _, row := range rows {
			out = append(out, domain.Channel{
				ID: row.ID, AccessHash: row.AccessHash, Title: row.Title,
				Broadcast: row.Broadcast, Megagroup: row.Megagroup, Deleted: true,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func channelBannedSenderChatFromRow(row sqlcgen.ChannelBannedSenderChat) domain.ChannelBannedSenderChat {
	return domain.ChannelBannedSenderChat{
		ChannelID:      row.ChannelID,
		SenderChatID:   row.SenderChatID,
		BannedByUserID: row.BannedByUserID,
		Date:           int(row.BannedAt),
	}
}
