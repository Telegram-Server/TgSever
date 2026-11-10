package memory

import (
	"bytes"
	"context"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

var _ store.ChannelImportStore = (*ChannelStore)(nil)

// channelImportState 是内存后端的频道导入状态；按需建表，零值可用。
type channelImportState struct {
	nextID  int64
	imports map[int64]domain.ChannelImport
	// byChannel: channel id → import id。
	byChannel map[int64]int64
	// messageMap: channel id → 来源消息 id → 映射。
	messageMap map[int64]map[int]channelImportMapping
	// media: import id → 来源文件标识 → 本地媒体。
	media map[int64]map[string]domain.ChannelImportMedia
}

type channelImportMapping struct {
	localID int
	hash    []byte
}

func (s *ChannelStore) importStateLocked() *channelImportState {
	if s.imports == nil {
		s.imports = &channelImportState{
			imports:    make(map[int64]domain.ChannelImport),
			byChannel:  make(map[int64]int64),
			messageMap: make(map[int64]map[int]channelImportMapping),
			media:      make(map[int64]map[string]domain.ChannelImportMedia),
		}
	}
	return s.imports
}

func (s *ChannelStore) maxChannelMessageIDLocked(channelID int64) int {
	maxID := 0
	for _, msg := range s.messages[channelID] {
		if msg.ID > maxID {
			maxID = msg.ID
		}
	}
	return maxID
}

func (s *ChannelStore) CreateChannelImport(_ context.Context, imp domain.ChannelImport) (domain.ChannelImport, error) {
	if imp.ChannelID == 0 || imp.SenderUserID == 0 {
		return domain.ChannelImport{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, ok := s.channels[imp.ChannelID]
	if !ok {
		return domain.ChannelImport{}, domain.ErrChannelInvalid
	}
	if channel.Deleted || !channel.Broadcast {
		return domain.ChannelImport{}, domain.ErrChannelImportTarget
	}
	st := s.importStateLocked()
	if _, exists := st.byChannel[imp.ChannelID]; exists {
		return domain.ChannelImport{}, domain.ErrChannelImportExists
	}
	if s.maxChannelMessageIDLocked(imp.ChannelID) > 1 {
		return domain.ChannelImport{}, domain.ErrChannelImportTarget
	}
	if imp.SourceChannelID != 0 {
		for _, other := range st.imports {
			if other.SourceChannelID == imp.SourceChannelID && other.State != domain.ChannelImportDetached {
				return domain.ChannelImport{}, domain.ErrChannelImportExists
			}
		}
	}
	st.nextID++
	imp.ID = st.nextID
	imp.State = domain.ChannelImportImporting
	imp.MaxSourceID = 0
	imp.CreatedAt = int(time.Now().Unix())
	imp.FinalizedAt = 0
	st.imports[imp.ID] = imp
	st.byChannel[imp.ChannelID] = imp.ID
	return imp, nil
}

func (s *ChannelStore) GetChannelImport(_ context.Context, importID int64) (domain.ChannelImport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	imp, ok := s.importStateLocked().imports[importID]
	if !ok {
		return domain.ChannelImport{}, domain.ErrChannelImportNotFound
	}
	return imp, nil
}

func (s *ChannelStore) FindChannelImportBySource(_ context.Context, sourceChannelID int64) (domain.ChannelImport, bool, error) {
	if sourceChannelID == 0 {
		return domain.ChannelImport{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, imp := range s.importStateLocked().imports {
		if imp.SourceChannelID == sourceChannelID && imp.State != domain.ChannelImportDetached {
			return imp, true, nil
		}
	}
	return domain.ChannelImport{}, false, nil
}

func (s *ChannelStore) SetChannelImportState(_ context.Context, importID int64, state domain.ChannelImportState) (domain.ChannelImport, error) {
	switch state {
	case domain.ChannelImportImporting, domain.ChannelImportMirroring, domain.ChannelImportDetached:
	default:
		return domain.ChannelImport{}, domain.ErrChannelImportState
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.importStateLocked()
	imp, ok := st.imports[importID]
	if !ok {
		return domain.ChannelImport{}, domain.ErrChannelImportNotFound
	}
	imp.State = state
	if state == domain.ChannelImportMirroring && imp.FinalizedAt == 0 {
		imp.FinalizedAt = int(time.Now().Unix())
	}
	st.imports[importID] = imp
	return imp, nil
}

func (s *ChannelStore) ChannelImportStats(_ context.Context, importID int64) (domain.ChannelImportStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.importStateLocked()
	imp, ok := st.imports[importID]
	if !ok {
		return domain.ChannelImportStats{}, domain.ErrChannelImportNotFound
	}
	return domain.ChannelImportStats{Messages: len(st.messageMap[imp.ChannelID]), Media: len(st.media[importID])}, nil
}

func (s *ChannelStore) ImportChannelMessages(_ context.Context, req domain.ImportChannelMessagesRequest) (domain.ImportChannelMessagesResult, error) {
	if req.ImportID == 0 {
		return domain.ImportChannelMessagesResult{}, domain.ErrChannelImportNotFound
	}
	if err := store.ValidateChannelImportBatch(req.Messages); err != nil {
		return domain.ImportChannelMessagesResult{}, err
	}
	if req.Now == 0 {
		req.Now = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.importStateLocked()
	imp, ok := st.imports[req.ImportID]
	if !ok {
		return domain.ImportChannelMessagesResult{}, domain.ErrChannelImportNotFound
	}
	if imp.State == domain.ChannelImportDetached {
		return domain.ImportChannelMessagesResult{}, domain.ErrChannelImportState
	}
	channel, ok := s.channels[imp.ChannelID]
	if !ok || channel.Deleted {
		return domain.ImportChannelMessagesResult{}, domain.ErrChannelInvalid
	}
	// 与 postgres 的事务语义对齐：先整批校验（顺序、摘要），再开始改状态，失败的一批不留痕迹。
	hashes := make([][]byte, len(req.Messages))
	mapping := st.messageMap[imp.ChannelID]
	for i, in := range req.Messages {
		hash, err := store.ChannelImportContentHash(in)
		if err != nil {
			return domain.ImportChannelMessagesResult{}, err
		}
		hashes[i] = hash
		if _, exists := mapping[in.SourceID]; !exists && in.SourceID < imp.MaxSourceID {
			return domain.ImportChannelMessagesResult{}, domain.ErrChannelImportOrder
		}
	}
	if mapping == nil {
		mapping = make(map[int]channelImportMapping)
		st.messageMap[imp.ChannelID] = mapping
	}
	live := imp.State == domain.ChannelImportMirroring
	result := domain.ImportChannelMessagesResult{LocalIDs: make(map[int]int, len(req.Messages))}
	batchPts := 0
	nextPts := func() int {
		if live {
			return s.nextChannelPtsLocked(imp.ChannelID)
		}
		if batchPts == 0 {
			batchPts = s.nextChannelPtsLocked(imp.ChannelID)
		}
		return batchPts
	}
	if s.msgViews[imp.ChannelID] == nil {
		s.msgViews[imp.ChannelID] = make(map[int]int)
	}
	var top domain.ChannelMessage
	pinnedTouched := false
	for i, in := range req.Messages {
		if existing, exists := mapping[in.SourceID]; exists {
			result.LocalIDs[in.SourceID] = existing.localID
			idx, found := s.findMessageIndexLocked(imp.ChannelID, existing.localID)
			if !found {
				result.Unchanged++
				continue
			}
			msg := s.messages[imp.ChannelID][idx]
			if in.Views > s.msgViews[imp.ChannelID][msg.ID] {
				s.msgViews[imp.ChannelID][msg.ID] = in.Views
			}
			msg.ViewsCount = s.msgViews[imp.ChannelID][msg.ID]
			if in.Forwards > msg.ForwardsCount {
				msg.ForwardsCount = in.Forwards
			}
			msg.Pinned = in.Pinned
			if bytes.Equal(existing.hash, hashes[i]) {
				s.messages[imp.ChannelID][idx] = msg
				result.Unchanged++
				continue
			}
			mapping[in.SourceID] = channelImportMapping{localID: existing.localID, hash: hashes[i]}
			pinnedTouched = true
			result.Edited++
			if msg.Deleted {
				s.messages[imp.ChannelID][idx] = msg
				continue
			}
			pts := nextPts()
			msg.Body = in.Body
			msg.Entities = append([]domain.MessageEntity(nil), in.Entities...)
			msg.Media = in.Media
			msg.ReplyMarkup = cloneReplyMarkup(in.ReplyMarkup)
			msg.EditDate = in.EditDate
			msg.PostAuthor = in.PostAuthor
			msg.Pts = pts
			s.messages[imp.ChannelID][idx] = msg
			if live {
				event := domain.ChannelUpdateEvent{
					ChannelID:    imp.ChannelID,
					Type:         domain.ChannelUpdateEditMessage,
					Pts:          pts,
					PtsCount:     1,
					Date:         req.Now,
					Message:      cloneChannelMessage(msg),
					SenderUserID: imp.SenderUserID,
				}
				s.appendChannelEventLocked(event)
				result.Events = append(result.Events, cloneChannelEvent(event))
			}
			continue
		}
		// 来源 id 还没被本地占用就沿用，否则分配下一个本地 id。
		localID := in.SourceID
		if localID > s.msgSeq[imp.ChannelID] {
			s.msgSeq[imp.ChannelID] = localID
		} else {
			localID = s.nextChannelMessageIDLocked(imp.ChannelID)
		}
		pts := nextPts()
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
			ReplyMarkup:   cloneReplyMarkup(in.ReplyMarkup),
			Forward:       cloneMessageForward(in.Forward),
			PostAuthor:    in.PostAuthor,
			Action:        cloneChannelMessageAction(in.Action),
			Pinned:        in.Pinned,
			ViewsCount:    in.Views,
			ForwardsCount: in.Forwards,
			Pts:           pts,
		}
		if in.ReplyToSourceID > 0 {
			replyID := in.ReplyToSourceID
			if target, ok := mapping[in.ReplyToSourceID]; ok {
				replyID = target.localID
			}
			msg.ReplyTo = &domain.MessageReply{MessageID: replyID}
		}
		s.messages[imp.ChannelID] = append(s.messages[imp.ChannelID], msg)
		s.msgViews[imp.ChannelID][localID] = in.Views
		mapping[in.SourceID] = channelImportMapping{localID: localID, hash: hashes[i]}
		result.LocalIDs[in.SourceID] = localID
		result.Written++
		pinnedTouched = pinnedTouched || in.Pinned
		top = msg
		if live {
			event := domain.ChannelUpdateEvent{
				ChannelID:    imp.ChannelID,
				Type:         domain.ChannelUpdateNewMessage,
				Pts:          pts,
				PtsCount:     1,
				Date:         req.Now,
				Message:      cloneChannelMessage(msg),
				SenderUserID: imp.SenderUserID,
			}
			s.appendChannelEventLocked(event)
			result.Events = append(result.Events, cloneChannelEvent(event))
		}
	}
	channel = s.channels[imp.ChannelID]
	channel.Pts = s.ptsSeq[imp.ChannelID]
	if top.ID > channel.TopMessageID {
		channel.TopMessageID = top.ID
	}
	if pinnedTouched {
		channel.PinnedMessageID = s.latestPinnedMessageIDLocked(imp.ChannelID)
	}
	s.channels[imp.ChannelID] = channel
	if !live && batchPts > 0 {
		// 历史回填不写逐条事件：把保留下界推到当前 pts，游标更旧的客户端拿到 tooLong 重新拉取。
		checkpoint := s.retention[imp.ChannelID]
		checkpoint.ChannelID = imp.ChannelID
		if batchPts > checkpoint.RetainedThroughPts {
			checkpoint.RetainedThroughPts = batchPts
		}
		if batchPts > checkpoint.LatestPts {
			checkpoint.LatestPts = batchPts
		}
		if req.Now > checkpoint.LatestEventDate {
			checkpoint.LatestEventDate = req.Now
		}
		s.retention[imp.ChannelID] = checkpoint
		kept := s.events[imp.ChannelID][:0]
		for _, event := range s.events[imp.ChannelID] {
			if event.Pts > batchPts {
				kept = append(kept, event)
			}
		}
		s.events[imp.ChannelID] = kept
	}
	if top.ID != 0 {
		for userID, member := range s.members[imp.ChannelID] {
			if member.Status == domain.ChannelMemberActive {
				s.upsertChannelDialogLocked(userID, channel, top, false)
			}
		}
	}
	if last := req.Messages[len(req.Messages)-1].SourceID; last > imp.MaxSourceID {
		imp.MaxSourceID = last
	}
	st.imports[imp.ID] = imp
	result.Import = imp
	result.Channel = channel
	return result, nil
}

func (s *ChannelStore) ResolveChannelImportMessageIDs(_ context.Context, importID int64, sourceIDs []int) (map[int]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.importStateLocked()
	imp, ok := st.imports[importID]
	if !ok {
		return nil, domain.ErrChannelImportNotFound
	}
	out := make(map[int]int, len(sourceIDs))
	for _, id := range sourceIDs {
		if m, ok := st.messageMap[imp.ChannelID][id]; ok {
			out[id] = m.localID
		}
	}
	return out, nil
}

func (s *ChannelStore) SetChannelImportMeta(_ context.Context, channelID int64, title, about *string) (domain.Channel, error) {
	if title != nil && !domain.ValidChannelTitle(*title) {
		return domain.Channel{}, domain.ErrChannelTitleInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, ok := s.channels[channelID]
	if !ok || channel.Deleted {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	if title != nil {
		channel.Title = *title
	}
	if about != nil {
		channel.About = *about
	}
	s.channels[channelID] = channel
	return channel, nil
}

func (s *ChannelStore) RewriteChannelCreateMessage(_ context.Context, req domain.RewriteChannelCreateRequest) error {
	if req.ChannelID == 0 || req.Date <= 0 || req.Title == "" {
		return domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, found := s.findMessageIndexLocked(req.ChannelID, 1)
	if !found {
		return domain.ErrChannelImportTarget
	}
	msg := s.messages[req.ChannelID][idx]
	if msg.Action == nil || msg.Action.Type != domain.ChannelActionCreate {
		return domain.ErrChannelImportTarget
	}
	action := cloneChannelMessageAction(msg.Action)
	action.Title = req.Title
	msg.Action = action
	msg.Date = req.Date
	msg.Deleted = req.Drop
	s.messages[req.ChannelID][idx] = msg
	channel := s.channels[req.ChannelID]
	channel.Date = req.Date
	s.channels[req.ChannelID] = channel
	return nil
}

func (s *ChannelStore) MarkChannelImportRead(_ context.Context, channelID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, ok := s.channels[channelID]
	if !ok {
		return domain.ErrChannelInvalid
	}
	maxID := s.maxChannelMessageIDLocked(channelID)
	for userID, member := range s.members[channelID] {
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		if maxID > member.ReadInboxMaxID {
			member.ReadInboxMaxID = maxID
		}
		member.UnreadMark = false
		s.members[channelID][userID] = member
		s.upsertChannelDialogLocked(userID, channel, domain.ChannelMessage{}, false)
	}
	return nil
}

func (s *ChannelStore) ReserveChannelMessageIDs(_ context.Context, channelID int64, through int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channelID != 0 && through > s.msgSeq[channelID] {
		s.msgSeq[channelID] = through
	}
	return nil
}

func (s *ChannelStore) GetChannelImportMedia(_ context.Context, importID int64, sourceKey string) (domain.ChannelImportMedia, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	media, ok := s.importStateLocked().media[importID][sourceKey]
	return media, ok, nil
}

func (s *ChannelStore) PutChannelImportMedia(_ context.Context, importID int64, sourceKey string, media domain.ChannelImportMedia) (domain.ChannelImportMedia, error) {
	if importID == 0 || sourceKey == "" || media.MediaID == 0 {
		return domain.ChannelImportMedia{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.importStateLocked()
	if existing, ok := st.media[importID][sourceKey]; ok {
		return existing, nil
	}
	if st.media[importID] == nil {
		st.media[importID] = make(map[string]domain.ChannelImportMedia)
	}
	st.media[importID][sourceKey] = media
	return media, nil
}
