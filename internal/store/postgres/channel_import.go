package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

var _ store.ChannelImportStore = (*ChannelStore)(nil)

// channelCreateMessageID 是“频道已创建”服务消息的 id：建频道时分配的第一个消息 id。
const channelCreateMessageID = 1

func channelImportFromRow(id, channelID int64, state string, sourceChannelID int64, sourceUsername string, senderUserID int64, maxSourceID int32, createdAt, finalizedAt int64) domain.ChannelImport {
	return domain.ChannelImport{
		ID:              id,
		ChannelID:       channelID,
		State:           domain.ChannelImportState(state),
		SourceChannelID: sourceChannelID,
		SourceUsername:  sourceUsername,
		SenderUserID:    senderUserID,
		MaxSourceID:     int(maxSourceID),
		CreatedAt:       int(createdAt),
		FinalizedAt:     int(finalizedAt),
	}
}

func (s *ChannelStore) CreateChannelImport(ctx context.Context, imp domain.ChannelImport) (domain.ChannelImport, error) {
	if imp.ChannelID == 0 || imp.SenderUserID == 0 {
		return domain.ChannelImport{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ChannelImport{}, fmt.Errorf("create channel import: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ChannelImport{}, fmt.Errorf("begin create channel import: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	channel, err := getChannelByID(ctx, tx, imp.ChannelID)
	if err != nil {
		return domain.ChannelImport{}, err
	}
	if channel.Deleted || !channel.Broadcast {
		return domain.ChannelImport{}, domain.ErrChannelImportTarget
	}
	row, err := q.InsertChannelImport(ctx, sqlcgen.InsertChannelImportParams{
		ChannelID:       imp.ChannelID,
		SourceChannelID: imp.SourceChannelID,
		SourceUsername:  imp.SourceUsername,
		SenderUserID:    imp.SenderUserID,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ChannelImport{}, domain.ErrChannelImportExists
		}
		return domain.ChannelImport{}, fmt.Errorf("insert channel import: %w", err)
	}
	// 先插入任务行再查目标是否为空：频道已有任务时报“已存在”，而不是“目标不合法”。
	// 来源消息 id 要原样落到本地：目标频道除了“频道已创建”之外不能已经有消息。
	maxID, err := q.MaxChannelMessageID(ctx, imp.ChannelID)
	if err != nil {
		return domain.ChannelImport{}, fmt.Errorf("create channel import: max message id: %w", err)
	}
	if maxID > channelCreateMessageID {
		return domain.ChannelImport{}, domain.ErrChannelImportTarget
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelImport{}, fmt.Errorf("commit create channel import: %w", err)
	}
	return channelImportFromRow(row.ID, row.ChannelID, row.State, row.SourceChannelID, row.SourceUsername, row.SenderUserID, row.MaxSourceID, row.CreatedAt, row.FinalizedAt), nil
}

func (s *ChannelStore) GetChannelImport(ctx context.Context, importID int64) (domain.ChannelImport, error) {
	row, err := sqlcgen.New(s.db).GetChannelImport(ctx, importID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelImport{}, domain.ErrChannelImportNotFound
	}
	if err != nil {
		return domain.ChannelImport{}, fmt.Errorf("get channel import: %w", err)
	}
	return channelImportFromRow(row.ID, row.ChannelID, row.State, row.SourceChannelID, row.SourceUsername, row.SenderUserID, row.MaxSourceID, row.CreatedAt, row.FinalizedAt), nil
}

func (s *ChannelStore) FindChannelImportBySource(ctx context.Context, sourceChannelID int64) (domain.ChannelImport, bool, error) {
	if sourceChannelID == 0 {
		return domain.ChannelImport{}, false, nil
	}
	row, err := sqlcgen.New(s.db).GetActiveChannelImportBySource(ctx, sourceChannelID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelImport{}, false, nil
	}
	if err != nil {
		return domain.ChannelImport{}, false, fmt.Errorf("find channel import by source: %w", err)
	}
	return channelImportFromRow(row.ID, row.ChannelID, row.State, row.SourceChannelID, row.SourceUsername, row.SenderUserID, row.MaxSourceID, row.CreatedAt, row.FinalizedAt), true, nil
}

func (s *ChannelStore) SetChannelImportState(ctx context.Context, importID int64, state domain.ChannelImportState) (domain.ChannelImport, error) {
	switch state {
	case domain.ChannelImportImporting, domain.ChannelImportMirroring, domain.ChannelImportDetached:
	default:
		return domain.ChannelImport{}, domain.ErrChannelImportState
	}
	row, err := sqlcgen.New(s.db).SetChannelImportState(ctx, sqlcgen.SetChannelImportStateParams{State: string(state), ID: importID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelImport{}, domain.ErrChannelImportNotFound
	}
	if err != nil {
		return domain.ChannelImport{}, fmt.Errorf("set channel import state: %w", err)
	}
	return channelImportFromRow(row.ID, row.ChannelID, row.State, row.SourceChannelID, row.SourceUsername, row.SenderUserID, row.MaxSourceID, row.CreatedAt, row.FinalizedAt), nil
}

func (s *ChannelStore) ChannelImportStats(ctx context.Context, importID int64) (domain.ChannelImportStats, error) {
	imp, err := s.GetChannelImport(ctx, importID)
	if err != nil {
		return domain.ChannelImportStats{}, err
	}
	q := sqlcgen.New(s.db)
	messages, err := q.CountChannelImportMessages(ctx, imp.ChannelID)
	if err != nil {
		return domain.ChannelImportStats{}, fmt.Errorf("count channel import messages: %w", err)
	}
	media, err := q.CountChannelImportMedia(ctx, importID)
	if err != nil {
		return domain.ChannelImportStats{}, fmt.Errorf("count channel import media: %w", err)
	}
	return domain.ChannelImportStats{Messages: int(messages), Media: int(media)}, nil
}

func (s *ChannelStore) ImportChannelMessages(ctx context.Context, req domain.ImportChannelMessagesRequest) (domain.ImportChannelMessagesResult, error) {
	if req.ImportID == 0 {
		return domain.ImportChannelMessagesResult{}, domain.ErrChannelImportNotFound
	}
	if err := store.ValidateChannelImportBatch(req.Messages); err != nil {
		return domain.ImportChannelMessagesResult{}, err
	}
	if req.Now == 0 {
		req.Now = nowUnix()
	}
	var lastErr error
	for attempt := 0; attempt < retryableChannelTxAttempts; attempt++ {
		res, err := s.importChannelMessagesOnce(ctx, req)
		if err == nil || !isRetryablePostgresTxError(err) || ctx.Err() != nil {
			return res, err
		}
		lastErr = err
	}
	return domain.ImportChannelMessagesResult{}, lastErr
}

func (s *ChannelStore) importChannelMessagesOnce(ctx context.Context, req domain.ImportChannelMessagesRequest) (domain.ImportChannelMessagesResult, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ImportChannelMessagesResult{}, fmt.Errorf("import channel messages: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ImportChannelMessagesResult{}, fmt.Errorf("begin import channel messages: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)

	row, err := q.GetChannelImportForUpdate(ctx, req.ImportID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ImportChannelMessagesResult{}, domain.ErrChannelImportNotFound
	}
	if err != nil {
		return domain.ImportChannelMessagesResult{}, fmt.Errorf("lock channel import: %w", err)
	}
	imp := channelImportFromRow(row.ID, row.ChannelID, row.State, row.SourceChannelID, row.SourceUsername, row.SenderUserID, row.MaxSourceID, row.CreatedAt, row.FinalizedAt)
	if imp.State == domain.ChannelImportDetached {
		return domain.ImportChannelMessagesResult{}, domain.ErrChannelImportState
	}
	channel, err := getChannelByID(ctx, tx, imp.ChannelID)
	if err != nil {
		return domain.ImportChannelMessagesResult{}, err
	}
	if channel.Deleted {
		return domain.ImportChannelMessagesResult{}, domain.ErrChannelInvalid
	}
	live := imp.State == domain.ChannelImportMirroring

	// 一次读出本批消息自身与它们回复目标的已有映射。
	lookup := make([]int, 0, len(req.Messages)*2)
	for _, msg := range req.Messages {
		lookup = append(lookup, msg.SourceID)
		if msg.ReplyToSourceID > 0 {
			lookup = append(lookup, msg.ReplyToSourceID)
		}
	}
	mapped, err := q.ListChannelImportMessageMap(ctx, sqlcgen.ListChannelImportMessageMapParams{
		ChannelID: imp.ChannelID,
		SourceIds: int32s(lookup),
	})
	if err != nil {
		return domain.ImportChannelMessagesResult{}, fmt.Errorf("list channel import map: %w", err)
	}
	localBySource := make(map[int]int, len(mapped))
	hashBySource := make(map[int][]byte, len(mapped))
	for _, m := range mapped {
		localBySource[int(m.SourceID)] = int(m.LocalID)
		hashBySource[int(m.SourceID)] = m.ContentHash
	}
	maxLocal32, err := q.MaxChannelMessageID(ctx, imp.ChannelID)
	if err != nil {
		return domain.ImportChannelMessagesResult{}, fmt.Errorf("import channel messages: max message id: %w", err)
	}
	maxLocal := int(maxLocal32)

	result := domain.ImportChannelMessagesResult{LocalIDs: make(map[int]int, len(req.Messages))}
	// importing 阶段整批共用一个 pts，第一次真正写入时才预留：全是重复消息的一批不推进 pts。
	batchPts := 0
	nextPts := func() (int, error) {
		if live {
			return s.reserveChannelPts(ctx, tx, imp.ChannelID)
		}
		if batchPts == 0 {
			pts, err := s.reserveChannelPts(ctx, tx, imp.ChannelID)
			if err != nil {
				return 0, err
			}
			batchPts = pts
		}
		return batchPts, nil
	}
	topID := 0
	pinnedTouched := false
	for _, in := range req.Messages {
		hash, err := store.ChannelImportContentHash(in)
		if err != nil {
			return domain.ImportChannelMessagesResult{}, err
		}
		if localID, exists := localBySource[in.SourceID]; exists {
			result.LocalIDs[in.SourceID] = localID
			if err := q.SetImportedChannelMessageCounters(ctx, sqlcgen.SetImportedChannelMessageCountersParams{
				ViewsCount:    int32(in.Views),
				ForwardsCount: int32(in.Forwards),
				Pinned:        in.Pinned,
				ChannelID:     imp.ChannelID,
				ID:            int32(localID),
			}); err != nil {
				return domain.ImportChannelMessagesResult{}, fmt.Errorf("refresh imported channel message counters: %w", err)
			}
			if bytes.Equal(hashBySource[in.SourceID], hash) {
				result.Unchanged++
				continue
			}
			pts, err := nextPts()
			if err != nil {
				return domain.ImportChannelMessagesResult{}, fmt.Errorf("allocate channel import edit pts: %w", err)
			}
			event, err := s.updateImportedChannelMessageTx(ctx, tx, imp, in, localID, pts, req.Now)
			if err != nil {
				return domain.ImportChannelMessagesResult{}, err
			}
			if err := q.UpsertChannelImportMessageMap(ctx, sqlcgen.UpsertChannelImportMessageMapParams{
				ChannelID: imp.ChannelID, SourceID: int32(in.SourceID), LocalID: int32(localID), ContentHash: hash,
			}); err != nil {
				return domain.ImportChannelMessagesResult{}, fmt.Errorf("update channel import map: %w", err)
			}
			pinnedTouched = true
			result.Edited++
			if live && event.Message.ID != 0 {
				if err := insertChannelEventTx(ctx, tx, event); err != nil {
					return domain.ImportChannelMessagesResult{}, err
				}
				result.Events = append(result.Events, event)
			}
			continue
		}
		if in.SourceID < imp.MaxSourceID {
			// 从未写入过、却排在已写入的最大来源 id 之前：历史只能按来源 id 从小到大回填。
			return domain.ImportChannelMessagesResult{}, domain.ErrChannelImportOrder
		}
		localID, err := s.importedChannelMessageID(ctx, tx, imp.ChannelID, in.SourceID, maxLocal)
		if err != nil {
			return domain.ImportChannelMessagesResult{}, err
		}
		maxLocal = localID
		pts, err := nextPts()
		if err != nil {
			return domain.ImportChannelMessagesResult{}, fmt.Errorf("allocate channel import pts: %w", err)
		}
		msg := domain.ChannelMessage{
			ChannelID:     imp.ChannelID,
			ID:            localID,
			SenderUserID:  imp.SenderUserID,
			From:          domain.Peer{Type: domain.PeerTypeUser, ID: imp.SenderUserID},
			Date:          in.Date,
			EditDate:      in.EditDate,
			Post:          channel.Broadcast,
			Silent:        in.Silent,
			NoForwards:    in.NoForwards,
			Body:          in.Body,
			Entities:      append([]domain.MessageEntity(nil), in.Entities...),
			Media:         in.Media,
			GroupedID:     in.GroupedID,
			ReplyMarkup:   in.ReplyMarkup,
			Forward:       cloneMessageForward(in.Forward),
			PostAuthor:    in.PostAuthor,
			Action:        cloneChannelMessageAction(in.Action),
			Pinned:        in.Pinned,
			ViewsCount:    in.Views,
			ForwardsCount: in.Forwards,
			Pts:           pts,
		}
		if in.ReplyToSourceID > 0 {
			// 回复目标查得到映射就换成本地 id；查不到原样保留——目标在来源已被删除，
			// 客户端显示为已删除的消息，与来源一致。
			replyID := in.ReplyToSourceID
			if mappedID, ok := localBySource[in.ReplyToSourceID]; ok {
				replyID = mappedID
			}
			msg.ReplyTo = &domain.MessageReply{MessageID: replyID}
		}
		// 导入的消息不写 expires_at：来源日期在过去，按频道 TTL 计算会被立刻清掉。
		if err := insertChannelMessageTx(ctx, tx, msg); err != nil {
			return domain.ImportChannelMessagesResult{}, err
		}
		if in.Views > 0 || in.Forwards > 0 || in.Pinned {
			if err := q.SetImportedChannelMessageCounters(ctx, sqlcgen.SetImportedChannelMessageCountersParams{
				ViewsCount:    int32(in.Views),
				ForwardsCount: int32(in.Forwards),
				Pinned:        in.Pinned,
				ChannelID:     imp.ChannelID,
				ID:            int32(localID),
			}); err != nil {
				return domain.ImportChannelMessagesResult{}, fmt.Errorf("set imported channel message counters: %w", err)
			}
		}
		if err := q.UpsertChannelImportMessageMap(ctx, sqlcgen.UpsertChannelImportMessageMapParams{
			ChannelID: imp.ChannelID, SourceID: int32(in.SourceID), LocalID: int32(localID), ContentHash: hash,
		}); err != nil {
			return domain.ImportChannelMessagesResult{}, fmt.Errorf("insert channel import map: %w", err)
		}
		localBySource[in.SourceID] = localID
		result.LocalIDs[in.SourceID] = localID
		result.Written++
		pinnedTouched = pinnedTouched || in.Pinned
		topID = localID
		if live {
			// 事件时间用服务器当前时间：按日期清理的事件保留期和离线补拉都以它为准，
			// 消息本身仍带来源日期。
			event := domain.ChannelUpdateEvent{
				ChannelID:    imp.ChannelID,
				Type:         domain.ChannelUpdateNewMessage,
				Pts:          pts,
				PtsCount:     1,
				Date:         req.Now,
				Message:      msg,
				SenderUserID: imp.SenderUserID,
			}
			if err := insertChannelEventTx(ctx, tx, event); err != nil {
				return domain.ImportChannelMessagesResult{}, err
			}
			result.Events = append(result.Events, event)
		}
	}
	if topID > 0 {
		if err := q.SetChannelTopMessageAtLeast(ctx, sqlcgen.SetChannelTopMessageAtLeastParams{TopMessageID: int32(topID), ChannelID: imp.ChannelID}); err != nil {
			return domain.ImportChannelMessagesResult{}, fmt.Errorf("update channel import top: %w", err)
		}
	}
	if pinnedTouched {
		if err := q.RefreshChannelPinnedMessageID(ctx, imp.ChannelID); err != nil {
			return domain.ImportChannelMessagesResult{}, fmt.Errorf("refresh channel pinned message: %w", err)
		}
	}
	if !live && batchPts > 0 {
		if err := q.AdvanceChannelUpdateFloor(ctx, sqlcgen.AdvanceChannelUpdateFloorParams{
			ChannelID: imp.ChannelID, Pts: int32(batchPts), EventDate: int32(req.Now),
		}); err != nil {
			return domain.ImportChannelMessagesResult{}, fmt.Errorf("advance channel update floor: %w", err)
		}
		if err := q.DeleteChannelUpdateEventsThrough(ctx, sqlcgen.DeleteChannelUpdateEventsThroughParams{ChannelID: imp.ChannelID, Pts: int32(batchPts)}); err != nil {
			return domain.ImportChannelMessagesResult{}, fmt.Errorf("drop channel events below import floor: %w", err)
		}
	}
	lastSourceID := req.Messages[len(req.Messages)-1].SourceID
	if err := q.BumpChannelImportMaxSourceID(ctx, sqlcgen.BumpChannelImportMaxSourceIDParams{MaxSourceID: int32(lastSourceID), ID: imp.ID}); err != nil {
		return domain.ImportChannelMessagesResult{}, fmt.Errorf("bump channel import max source id: %w", err)
	}
	if lastSourceID > imp.MaxSourceID {
		imp.MaxSourceID = lastSourceID
	}
	channel, err = getChannelByID(ctx, tx, imp.ChannelID)
	if err != nil {
		return domain.ImportChannelMessagesResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ImportChannelMessagesResult{}, fmt.Errorf("commit import channel messages: %w", err)
	}
	result.Import = imp
	result.Channel = channel
	return result, nil
}

// importedChannelMessageID 决定一条新来源消息的本地 id：来源 id 大于频道里已有的最大 id 且
// 能在分配器上占到号，就沿用；否则（本地发过帖、号已被占）分配下一个本地 id。
func (s *ChannelStore) importedChannelMessageID(ctx context.Context, tx pgx.Tx, channelID int64, sourceID, maxLocal int) (int, error) {
	if sourceID > maxLocal {
		claimed := true
		if claimer, ok := s.msgIDs.(store.ChannelMessageIDClaimer); ok {
			var err error
			claimed, err = claimer.ClaimChannelMessageID(withCounterDB(ctx, tx), channelID, sourceID)
			if err != nil {
				return 0, fmt.Errorf("claim channel message id %d: %w", sourceID, err)
			}
		}
		if claimed {
			return sourceID, nil
		}
	}
	id, err := s.nextChannelMessageID(ctx, tx, channelID)
	if err != nil {
		return 0, fmt.Errorf("allocate channel import message id: %w", err)
	}
	if id <= maxLocal {
		return 0, fmt.Errorf("allocate channel import message id: allocator returned %d, channel %d already has %d", id, channelID, maxLocal)
	}
	return id, nil
}

// updateImportedChannelMessageTx 用来源编辑后的内容替换一条已导入的消息，返回对应的编辑事件
// （调用方只在 mirroring 阶段落库并下发）。消息在本地已被删除时什么都不做，事件为零值。
func (s *ChannelStore) updateImportedChannelMessageTx(ctx context.Context, tx pgx.Tx, imp domain.ChannelImport, in domain.ImportedChannelMessage, localID, pts, now int) (domain.ChannelUpdateEvent, error) {
	current, err := s.getChannelMessage(ctx, tx, imp.ChannelID, localID)
	if errors.Is(err, domain.ErrMessageIDInvalid) || errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelUpdateEvent{}, nil
	}
	if err != nil {
		return domain.ChannelUpdateEvent{}, fmt.Errorf("load imported channel message %d: %w", localID, err)
	}
	if current.Deleted {
		return domain.ChannelUpdateEvent{}, nil
	}
	entities, err := encodeMessageEntities(in.Entities)
	if err != nil {
		return domain.ChannelUpdateEvent{}, err
	}
	media, err := encodeMessageMedia(in.Media)
	if err != nil {
		return domain.ChannelUpdateEvent{}, err
	}
	replyMarkup, err := encodeReplyMarkup(in.ReplyMarkup)
	if err != nil {
		return domain.ChannelUpdateEvent{}, err
	}
	if err := sqlcgen.New(tx).UpdateImportedChannelMessage(ctx, sqlcgen.UpdateImportedChannelMessageParams{
		Body:        in.Body,
		Entities:    entities,
		Media:       media,
		ReplyMarkup: replyMarkup,
		EditDate:    int32(in.EditDate),
		PostAuthor:  in.PostAuthor,
		Pts:         int32(pts),
		ChannelID:   imp.ChannelID,
		ID:          int32(localID),
	}); err != nil {
		return domain.ChannelUpdateEvent{}, fmt.Errorf("update imported channel message %d: %w", localID, err)
	}
	if err := replaceChannelMediaIndexTx(ctx, tx, imp.ChannelID, localID, current.Date, in.Media, in.Entities); err != nil {
		return domain.ChannelUpdateEvent{}, err
	}
	current.Body = in.Body
	current.Entities = append([]domain.MessageEntity(nil), in.Entities...)
	current.Media = in.Media
	current.ReplyMarkup = in.ReplyMarkup
	current.EditDate = in.EditDate
	current.PostAuthor = in.PostAuthor
	current.Pinned = in.Pinned
	current.Pts = pts
	return domain.ChannelUpdateEvent{
		ChannelID:    imp.ChannelID,
		Type:         domain.ChannelUpdateEditMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         now,
		Message:      current,
		SenderUserID: imp.SenderUserID,
	}, nil
}

func (s *ChannelStore) ResolveChannelImportMessageIDs(ctx context.Context, importID int64, sourceIDs []int) (map[int]int, error) {
	imp, err := s.GetChannelImport(ctx, importID)
	if err != nil {
		return nil, err
	}
	out := make(map[int]int, len(sourceIDs))
	if len(sourceIDs) == 0 {
		return out, nil
	}
	rows, err := sqlcgen.New(s.db).ListChannelImportMessageMap(ctx, sqlcgen.ListChannelImportMessageMapParams{
		ChannelID: imp.ChannelID,
		SourceIds: int32s(sourceIDs),
	})
	if err != nil {
		return nil, fmt.Errorf("resolve channel import message ids: %w", err)
	}
	for _, row := range rows {
		out[int(row.SourceID)] = int(row.LocalID)
	}
	return out, nil
}

func (s *ChannelStore) RewriteChannelCreateMessage(ctx context.Context, req domain.RewriteChannelCreateRequest) error {
	if req.ChannelID == 0 || req.Date <= 0 || req.Title == "" {
		return domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return fmt.Errorf("rewrite channel create message: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin rewrite channel create message: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	rows, err := q.RewriteChannelCreateMessage(ctx, sqlcgen.RewriteChannelCreateMessageParams{
		MessageDate: int32(req.Date),
		Title:       req.Title,
		Deleted:     req.Drop,
		ChannelID:   req.ChannelID,
		ID:          channelCreateMessageID,
	})
	if err != nil {
		return fmt.Errorf("rewrite channel create message: %w", err)
	}
	if rows == 0 {
		return domain.ErrChannelImportTarget
	}
	if err := q.SetChannelCreatedDate(ctx, sqlcgen.SetChannelCreatedDateParams{Date: int32(req.Date), ChannelID: req.ChannelID}); err != nil {
		return fmt.Errorf("set channel created date: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit rewrite channel create message: %w", err)
	}
	return nil
}

func (s *ChannelStore) MarkChannelImportRead(ctx context.Context, channelID int64) error {
	q := sqlcgen.New(s.db)
	maxID, err := q.MaxChannelMessageID(ctx, channelID)
	if err != nil {
		return fmt.Errorf("mark channel import read: max message id: %w", err)
	}
	if err := q.MarkChannelReadForAllMembers(ctx, sqlcgen.MarkChannelReadForAllMembersParams{MaxID: maxID, ChannelID: channelID}); err != nil {
		return fmt.Errorf("mark channel import read: %w", err)
	}
	return nil
}

func (s *ChannelStore) ReserveChannelMessageIDs(ctx context.Context, channelID int64, through int) error {
	if channelID == 0 || through <= 0 {
		return nil
	}
	// 只有带计数器的分配器能预留号段；按 MAX(id)+1 分配的回退分配器没有可推进的状态，
	// 预留在那里是空操作。
	claimer, ok := s.msgIDs.(store.ChannelMessageIDClaimer)
	if !ok {
		return nil
	}
	if _, err := claimer.ClaimChannelMessageID(ctx, channelID, through); err != nil {
		return fmt.Errorf("reserve channel message ids through %d: %w", through, err)
	}
	return nil
}

func (s *ChannelStore) GetChannelImportMedia(ctx context.Context, importID int64, sourceKey string) (domain.ChannelImportMedia, bool, error) {
	row, err := sqlcgen.New(s.db).GetChannelImportMedia(ctx, sqlcgen.GetChannelImportMediaParams{ImportID: importID, SourceKey: sourceKey})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelImportMedia{}, false, nil
	}
	if err != nil {
		return domain.ChannelImportMedia{}, false, fmt.Errorf("get channel import media: %w", err)
	}
	return domain.ChannelImportMedia{Kind: domain.ChannelImportMediaKind(row.Kind), MediaID: row.MediaID}, true, nil
}

func (s *ChannelStore) PutChannelImportMedia(ctx context.Context, importID int64, sourceKey string, media domain.ChannelImportMedia) (domain.ChannelImportMedia, error) {
	if importID == 0 || sourceKey == "" || media.MediaID == 0 {
		return domain.ChannelImportMedia{}, domain.ErrChannelInvalid
	}
	row, err := sqlcgen.New(s.db).PutChannelImportMedia(ctx, sqlcgen.PutChannelImportMediaParams{
		ImportID:  importID,
		SourceKey: sourceKey,
		Kind:      string(media.Kind),
		MediaID:   media.MediaID,
	})
	if err != nil {
		return domain.ChannelImportMedia{}, fmt.Errorf("put channel import media: %w", err)
	}
	return domain.ChannelImportMedia{Kind: domain.ChannelImportMediaKind(row.Kind), MediaID: row.MediaID}, nil
}
