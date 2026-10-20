package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func (s *ChannelStore) GetParticipants(_ context.Context, viewerUserID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, viewer, _, err := s.channelForViewerLocked(viewerUserID, channelID)
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	if limit <= 0 || limit > domain.MaxChannelParticipantsLimit {
		limit = domain.MaxChannelParticipantsLimit
	}
	// 广播频道订阅者列表仅管理员可枚举（与隐藏成员同一门控）：admins filter 仍放行（徽章数据源）。
	if channel.MembersListAdminOnly() && !isChannelAdmin(viewer) {
		switch filter.Kind {
		case domain.ChannelParticipantsAdmins:
		case domain.ChannelParticipantsBots:
			return domain.ChannelParticipantList{Channel: channel, Count: 0}, nil
		default:
			return domain.ChannelParticipantList{Channel: channel, Count: channel.ParticipantsCount}, nil
		}
	}
	if (filter.Kind == domain.ChannelParticipantsBanned || filter.Kind == domain.ChannelParticipantsKicked) && !isChannelAdmin(viewer) {
		return domain.ChannelParticipantList{Channel: channel}, nil
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	items := make([]domain.ChannelMember, 0, len(s.members[channelID]))
	now := restrictionNow(0)
	for _, member := range s.members[channelID] {
		member = member.EffectiveAt(now)
		if !channelParticipantMatchesFilter(member, filter.Kind, query) {
			continue
		}
		if shouldHideAnonymousAdminFromParticipantList(viewer, member) {
			continue
		}
		items = append(items, member)
	}
	if filter.Kind == domain.ChannelParticipantsKicked {
		for senderID, member := range s.senderBans[channelID] {
			sender := s.channels[senderID]
			member.SenderChannelIdentity = domain.SenderIdentity(sender)
			if query == "" || strings.Contains(strings.ToLower(sender.Title), query) || strings.Contains(strings.ToLower(sender.Username), query) || strings.Contains(strconv.FormatInt(senderID, 10), query) {
				items = append(items, member)
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Role != items[j].Role {
			return channelRoleOrder(items[i].Role) < channelRoleOrder(items[j].Role)
		}
		if items[i].UserID == items[j].UserID {
			return items[i].Peer.ID < items[j].Peer.ID
		}
		return items[i].UserID < items[j].UserID
	})
	count := len(items)
	if offset < 0 {
		offset = 0
	}
	if offset > domain.MaxChannelParticipantsOffset {
		offset = domain.MaxChannelParticipantsOffset
	}
	if offset >= len(items) {
		items = nil
	} else {
		end := offset + limit
		if end > len(items) {
			end = len(items)
		}
		items = items[offset:end]
	}
	return domain.ChannelParticipantList{
		Channel:      channel,
		Participants: cloneChannelMembers(items),
		Count:        count,
	}, nil
}

func (s *ChannelStore) GetParticipant(_ context.Context, viewerUserID, channelID, participantUserID int64) (domain.ChannelMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, viewer, _, err := s.channelForViewerLocked(viewerUserID, channelID)
	if err != nil {
		return domain.ChannelMember{}, err
	}
	if viewerUserID == participantUserID && viewer.Guest {
		return domain.ChannelMember{}, domain.ErrUserNotParticipant
	}
	member, ok := s.effectiveMemberLocked(channelID, participantUserID, restrictionNow(0))
	if !ok || (participantUserID == viewerUserID && member.Status == domain.ChannelMemberLeft) {
		return domain.ChannelMember{}, domain.ErrUserNotParticipant
	}
	return member, nil
}

// restrictionNow 返回换算限制期限用的当前时刻：操作带请求 Date 时用它（与同一操作里写入的
// 时间一致），没有时用真实时钟。
func restrictionNow(date int) int {
	if date > 0 {
		return date
	}
	return int(time.Now().Unix())
}

// effectiveMemberLocked 返回成员记录在 now 时刻生效的视图（见 domain.ChannelMember.EffectiveAt）：
// 存储里保留管理员设置时的原始限制，期限到了的限制在读取时才视为解除。
func (s *ChannelStore) effectiveMemberLocked(channelID, userID int64, now int) (domain.ChannelMember, bool) {
	member, ok := s.members[channelID][userID]
	if !ok {
		return domain.ChannelMember{}, false
	}
	return member.EffectiveAt(now), true
}

func (s *ChannelStore) FutureCreatorAfterLeave(_ context.Context, channelID, userID int64) (domain.ChannelMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(userID, channelID)
	if err != nil {
		return domain.ChannelMember{}, err
	}
	if channel.CreatorUserID != userID || member.Role != domain.ChannelRoleCreator {
		return domain.ChannelMember{}, domain.ErrChannelAdminRequired
	}
	return s.futureCreatorAfterLeaveLocked(channelID, userID)
}

func (s *ChannelStore) JoinChannel(_ context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, ok := s.channels[channelID]
	if !ok || channel.Deleted {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	if channel.Monoforum {
		return domain.CreateChannelResult{}, domain.ErrChannelMonoforumUnsupported
	}
	preJoinTopID := channel.TopMessageID
	if existing, ok := s.effectiveMemberLocked(channelID, userID, restrictionNow(date)); ok {
		if existing.Status == domain.ChannelMemberActive {
			return domain.CreateChannelResult{}, domain.ErrUserAlreadyParticipant
		}
		if existing.IsKicked() {
			return domain.CreateChannelResult{}, domain.ErrChannelUserBanned
		}
	}
	if channel.JoinRequest {
		if err := s.recordPublicJoinRequestLocked(channel, userID, date); err != nil {
			return domain.CreateChannelResult{}, err
		}
		return domain.CreateChannelResult{Channel: channel, JoinRequest: domain.NewChannelJoinRequest(channel.ID, userID, date, nil)}, domain.ErrInviteRequestSent
	}
	member := domain.ChannelMember{
		ChannelID: channelID,
		UserID:    userID,
		// 自加入：inviter 即本人（对齐官方 channelParticipantSelf.inviter_id == user_id），
		// 客户端据此生成本地「您加入了此频道」服务消息把广播频道补进会话列表。见 postgres 同处注释。
		InviterUserID: userID,
		Role:          domain.ChannelRoleMember,
		Status:        domain.ChannelMemberActive,
		JoinedAt:      date,
	}
	var previous map[int64]domain.ChannelMember
	if existing, ok := s.effectiveMemberLocked(channelID, userID, restrictionNow(date)); ok {
		previous = map[int64]domain.ChannelMember{userID: existing}
		member = existing
		member.Status = domain.ChannelMemberActive
		member.LeftAt = 0
		member.BannedRights = rejoinBannedRights(existing, date)
		// 重进是全新 participant：不保留旧的角色/管理权/Tag；inviter 重置为本人（自加入）。
		// 只有 channel 当前 owner 仍是该账号时，才保留 creator 身份。
		if existing.Role != domain.ChannelRoleCreator || channel.CreatorUserID != userID {
			member.Role = domain.ChannelRoleMember
			member.AdminRights = domain.ChannelAdminRights{}
			member.Rank = ""
			member.InviterUserID = userID
			member.JoinedAt = date
		}
		if minID := channelInitialAvailableMinID(channel); minID > member.AvailableMinID {
			member.AvailableMinID = minID
			member.ReadInboxMaxID = maxInt(member.ReadInboxMaxID, minID)
		}
		member.ReadInboxMaxID = maxInt(member.ReadInboxMaxID, preJoinTopID)
		if minPts := channelInitialAvailableMinPts(channel); minPts > member.AvailableMinPts {
			member.AvailableMinPts = minPts
		}
		channel.ParticipantsCount++
	} else {
		member.AvailableMinID = channelInitialAvailableMinID(channel)
		member.AvailableMinPts = channelInitialAvailableMinPts(channel)
		member.ReadInboxMaxID = maxInt(member.AvailableMinID, preJoinTopID)
		channel.ParticipantsCount++
	}
	if s.members[channelID] == nil {
		s.members[channelID] = make(map[int64]domain.ChannelMember)
	}
	// 与 postgres 同口径:重新入群时归还纯 prehistory 来源的读边界。available_min_id 只增
	// 不减,不显式归零的话,"隐藏历史期间入群"的成员即使频道后来关闭隐藏历史也永远看不到
	// 旧消息,重新加入也救不回。带 history_clear_anchor 的边界来自成员自己的"清空历史",
	// 属 owner-local 语义,不回收。
	if !channel.PreHistoryHidden && member.AvailableMinID > 0 && member.HistoryClearAnchorID == 0 {
		member.AvailableMinID = 0
	}
	s.members[channelID][userID] = member
	s.channels[channelID] = channel
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID: channelID,
		UserID:    userID,
		Date:      date,
		Type:      domain.ChannelAdminLogParticipantJoin,
	})
	var msg domain.ChannelMessage
	var event domain.ChannelUpdateEvent
	if channel.Megagroup {
		msg, event = s.appendChannelServiceMessageLocked(channelID, userID, date, domain.ChannelMessageAction{
			Type:    domain.ChannelActionChatJoined,
			UserIDs: []int64{userID},
		})
		channel.TopMessageID = msg.ID
		channel.Pts = event.Pts
		s.channels[channelID] = channel
	}
	member.ReadInboxMaxID = maxInt(member.ReadInboxMaxID, channel.TopMessageID)
	if msg.ID != 0 && msg.SenderUserID == userID {
		member.ReadOutboxMaxID = maxInt(member.ReadOutboxMaxID, msg.ID)
	}
	s.members[channelID][userID] = member
	s.upsertChannelDialogLocked(userID, channel, msg, true)
	return domain.CreateChannelResult{
		Channel:    channel,
		Members:    []domain.ChannelMember{member},
		Previous:   previous,
		Message:    cloneChannelMessage(msg),
		Event:      cloneChannelEvent(event),
		Recipients: s.activeMemberIDsLocked(channelID, 0, 0),
	}, nil
}

func (s *ChannelStore) LeaveChannel(_ context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, member, err := s.channelAndMemberLocked(userID, channelID)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	members := []domain.ChannelMember{}
	previous := map[int64]domain.ChannelMember{userID: member}
	adminsDelta := 0
	if isChannelAdmin(member) {
		adminsDelta--
	}
	if channel.CreatorUserID == userID || member.Role == domain.ChannelRoleCreator {
		if channel.CreatorUserID != userID || member.Role != domain.ChannelRoleCreator {
			return domain.CreateChannelResult{}, domain.ErrChannelUserCreator
		}
		future, err := s.futureCreatorAfterLeaveLocked(channelID, userID)
		if err != nil {
			return domain.CreateChannelResult{}, domain.ErrChannelUserCreator
		}
		previous[future.UserID] = future
		if !isChannelAdmin(future) {
			adminsDelta++
		}
		future.Role = domain.ChannelRoleCreator
		future.AdminRights = creatorChannelAdminRights()
		future.Rank = ""
		future.Status = domain.ChannelMemberActive
		future.LeftAt = 0
		s.members[channelID][future.UserID] = future
		channel.CreatorUserID = future.UserID
		members = append(members, future)
		member.Role = domain.ChannelRoleMember
		member.AdminRights = domain.ChannelAdminRights{}
		member.Rank = ""
	}
	member.Status = domain.ChannelMemberLeft
	member.LeftAt = date
	s.members[channelID][userID] = member
	members = append([]domain.ChannelMember{member}, members...)
	s.clearChannelMentionsForUserLocked(channelID, userID)
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID: channelID,
		UserID:    userID,
		Date:      date,
		Type:      domain.ChannelAdminLogParticipantLeave,
	})
	if channel.ParticipantsCount > 0 {
		channel.ParticipantsCount--
	}
	channel.AdminsCount += adminsDelta
	if channel.AdminsCount < 0 {
		channel.AdminsCount = 0
	}
	var msg domain.ChannelMessage
	var event domain.ChannelUpdateEvent
	if channel.Megagroup {
		msg, event = s.appendChannelServiceMessageLocked(channelID, userID, date, domain.ChannelMessageAction{
			Type:    domain.ChannelActionChatDelete,
			UserIDs: []int64{userID},
		})
		channel.TopMessageID = msg.ID
		channel.Pts = event.Pts
	}
	s.channels[channelID] = channel
	return domain.CreateChannelResult{
		Channel:    channel,
		Members:    cloneChannelMembers(members),
		Previous:   previous,
		Message:    cloneChannelMessage(msg),
		Event:      cloneChannelEvent(event),
		Recipients: append(s.activeMemberIDsLocked(channelID, 0, 0), userID),
	}, nil
}

func (s *ChannelStore) EditChannelAdmin(_ context.Context, req domain.EditChannelAdminRequest) (domain.EditChannelAdminResult, error) {
	if req.HasRank() {
		if err := domain.ValidateChannelRank(req.Rank); err != nil {
			return domain.EditChannelAdminResult{}, err
		}
	}
	if req.UserID == 0 || req.ChannelID == 0 || req.MemberID == 0 {
		return domain.EditChannelAdminResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	actor := s.members[req.ChannelID][req.UserID]
	if !canAddChannelAdmins(actor) {
		return domain.EditChannelAdminResult{}, domain.ErrChannelAdminRequired
	}
	previous, ok := s.effectiveMemberLocked(req.ChannelID, req.MemberID, restrictionNow(req.Date))
	if !ok {
		previous = domain.ChannelMember{
			ChannelID:       req.ChannelID,
			UserID:          req.MemberID,
			InviterUserID:   req.UserID,
			Role:            domain.ChannelRoleMember,
			Status:          domain.ChannelMemberActive,
			JoinedAt:        req.Date,
			AvailableMinID:  channelInitialAvailableMinID(channel),
			AvailableMinPts: channelInitialAvailableMinPts(channel),
			ReadInboxMaxID:  channel.TopMessageID,
		}
	}
	if previous.Role == domain.ChannelRoleCreator {
		if req.MemberID != req.UserID || channel.CreatorUserID != req.UserID || actor.Role != domain.ChannelRoleCreator {
			return domain.EditChannelAdminResult{}, domain.ErrChannelUserCreator
		}
		member := previous
		member.Status = domain.ChannelMemberActive
		member.LeftAt = 0
		member.AdminRights = domain.CreatorProjectionAdminRights(req.AdminRights)
		if req.HasRank() {
			member.Rank = req.Rank
		}
		s.members[req.ChannelID][req.MemberID] = member
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID:       req.ChannelID,
			UserID:          req.UserID,
			Date:            req.Date,
			Type:            domain.ChannelAdminLogParticipantPromote,
			PrevParticipant: ptrChannelMember(previous),
			NewParticipant:  ptrChannelMember(member),
		})
		s.refreshChannelCountsLocked(req.ChannelID)
		channel = s.channels[req.ChannelID]
		event := transientChannelParticipantEvent(channel.ID, req.UserID, previous, member, req.Date)
		recipients := s.activeMemberIDsLocked(req.ChannelID, 0, 0)
		recipients = append(recipients, req.MemberID)
		return domain.EditChannelAdminResult{
			Channel:     channel,
			Previous:    previous,
			Participant: member,
			Event:       event,
			Recipients:  recipients,
			Date:        req.Date,
		}, nil
	}
	if err := domain.CheckManageChannelAdmin(actor, previous); err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	if actor.Role != domain.ChannelRoleCreator && !adminRightsSubset(req.AdminRights, actor.AdminRights) {
		return domain.EditChannelAdminResult{}, domain.ErrChannelRightForbidden
	}
	member := previous
	member.InviterUserID = req.UserID
	member.Status = domain.ChannelMemberActive
	member.LeftAt = 0
	if previous.Status != domain.ChannelMemberActive {
		if minPts := channelInitialAvailableMinPts(channel); minPts > member.AvailableMinPts {
			member.AvailableMinPts = minPts
		}
	}
	member.AdminRights = domain.NormalizeFullMegagroupAdminRights(channel, req.AdminRights)
	if req.HasRank() {
		member.Rank = req.Rank
	}
	if zeroChannelAdminRights(req.AdminRights) {
		member.Role = domain.ChannelRoleMember
		member.Rank = ""
	} else {
		member.Role = domain.ChannelRoleAdmin
	}
	// 被重新激活的成员(此前 left/kicked)同样要归还 prehistory 读边界,否则永久空频道。
	if previous.Status != domain.ChannelMemberActive && !channel.PreHistoryHidden &&
		member.AvailableMinID > 0 && member.HistoryClearAnchorID == 0 {
		member.AvailableMinID = 0
	}
	s.members[req.ChannelID][req.MemberID] = member
	logType := domain.ChannelAdminLogParticipantPromote
	if member.Role != domain.ChannelRoleAdmin {
		logType = domain.ChannelAdminLogParticipantDemote
	}
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID:       req.ChannelID,
		UserID:          req.UserID,
		Date:            req.Date,
		Type:            logType,
		PrevParticipant: ptrChannelMember(previous),
		NewParticipant:  ptrChannelMember(member),
	})
	s.refreshChannelCountsLocked(req.ChannelID)
	channel = s.channels[req.ChannelID]
	event := transientChannelParticipantEvent(channel.ID, req.UserID, previous, member, req.Date)
	if msg, ok := s.findMessageLocked(req.ChannelID, channel.TopMessageID); ok {
		s.upsertChannelDialogLocked(member.UserID, channel, msg, false)
	}
	recipients := s.activeMemberIDsLocked(req.ChannelID, 0, 0)
	recipients = append(recipients, req.MemberID)
	return domain.EditChannelAdminResult{
		Channel:     channel,
		Previous:    previous,
		Participant: member,
		Event:       event,
		Recipients:  recipients,
		Date:        req.Date,
	}, nil
}

func (s *ChannelStore) TransferChannelOwnership(_ context.Context, req domain.TransferChannelOwnershipRequest) (domain.TransferChannelOwnershipResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.NewOwnerID == 0 || req.NewOwnerID == req.UserID {
		return domain.TransferChannelOwnershipResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.TransferChannelOwnershipResult{}, err
	}
	previousOwner := s.members[req.ChannelID][req.UserID]
	if channel.CreatorUserID != req.UserID || previousOwner.Role != domain.ChannelRoleCreator {
		return domain.TransferChannelOwnershipResult{}, domain.ErrChannelAdminRequired
	}
	previousNewOwner, ok := s.members[req.ChannelID][req.NewOwnerID]
	if !ok || previousNewOwner.Status != domain.ChannelMemberActive || previousNewOwner.BannedRights.ViewMessages {
		return domain.TransferChannelOwnershipResult{}, domain.ErrUserNotParticipant
	}
	if previousNewOwner.Role == domain.ChannelRoleCreator {
		return domain.TransferChannelOwnershipResult{}, domain.ErrChannelNotModified
	}
	oldOwner := previousOwner
	oldOwner.Role = domain.ChannelRoleAdmin
	oldOwner.AdminRights = creatorChannelAdminRights()
	oldOwner.Rank = ""
	oldOwner.Status = domain.ChannelMemberActive
	oldOwner.LeftAt = 0
	if oldOwner.InviterUserID == 0 {
		oldOwner.InviterUserID = req.UserID
	}
	newOwner := previousNewOwner
	newOwner.Role = domain.ChannelRoleCreator
	newOwner.AdminRights = creatorChannelAdminRights()
	newOwner.Rank = ""
	newOwner.Status = domain.ChannelMemberActive
	newOwner.LeftAt = 0
	if newOwner.JoinedAt == 0 {
		newOwner.JoinedAt = req.Date
	}
	channel.CreatorUserID = req.NewOwnerID
	s.channels[req.ChannelID] = channel
	s.members[req.ChannelID][req.UserID] = oldOwner
	s.members[req.ChannelID][req.NewOwnerID] = newOwner
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID:       req.ChannelID,
		UserID:          req.UserID,
		Date:            req.Date,
		Type:            domain.ChannelAdminLogParticipantPromote,
		PrevParticipant: ptrChannelMember(previousOwner),
		NewParticipant:  ptrChannelMember(oldOwner),
	})
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID:       req.ChannelID,
		UserID:          req.UserID,
		Date:            req.Date,
		Type:            domain.ChannelAdminLogParticipantPromote,
		PrevParticipant: ptrChannelMember(previousNewOwner),
		NewParticipant:  ptrChannelMember(newOwner),
	})
	s.refreshChannelCountsLocked(req.ChannelID)
	channel = s.channels[req.ChannelID]
	if msg, ok := s.findMessageLocked(req.ChannelID, channel.TopMessageID); ok {
		s.upsertChannelDialogLocked(oldOwner.UserID, channel, msg, false)
		s.upsertChannelDialogLocked(newOwner.UserID, channel, msg, false)
	}
	events := []domain.ChannelUpdateEvent{
		transientChannelParticipantEvent(channel.ID, req.UserID, previousOwner, oldOwner, req.Date),
		transientChannelParticipantEvent(channel.ID, req.UserID, previousNewOwner, newOwner, req.Date),
	}
	recipients := s.activeMemberIDsLocked(req.ChannelID, 0, 0)
	recipients = append(recipients, req.UserID, req.NewOwnerID)
	return domain.TransferChannelOwnershipResult{
		Channel:          channel,
		PreviousOwner:    previousOwner,
		OldOwner:         oldOwner,
		PreviousNewOwner: previousNewOwner,
		NewOwner:         newOwner,
		Events:           events,
		Recipients:       recipients,
		Date:             req.Date,
	}, nil
}

func (s *ChannelStore) EditChannelMemberRank(_ context.Context, req domain.EditChannelMemberRankRequest) (domain.EditChannelAdminResult, error) {
	if err := domain.ValidateChannelRank(req.Rank); err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	if req.UserID == 0 || req.ChannelID == 0 || req.MemberID == 0 {
		return domain.EditChannelAdminResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	now := restrictionNow(req.Date)
	actor, _ := s.effectiveMemberLocked(req.ChannelID, req.UserID, now)
	previous, ok := s.effectiveMemberLocked(req.ChannelID, req.MemberID, now)
	if !ok || previous.Status != domain.ChannelMemberActive {
		return domain.EditChannelAdminResult{}, domain.ErrUserNotParticipant
	}
	if err := checkEditMemberRank(channel, actor, previous); err != nil {
		return domain.EditChannelAdminResult{}, err
	}
	member := previous
	member.Rank = req.Rank
	s.members[req.ChannelID][req.MemberID] = member
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID:   req.ChannelID,
		UserID:      req.UserID,
		Date:        req.Date,
		Type:        domain.ChannelAdminLogParticipantEditRank,
		PrevString:  previous.Rank,
		NewString:   member.Rank,
		Participant: ptrChannelMember(member),
	})
	event := transientChannelParticipantEvent(channel.ID, req.UserID, previous, member, req.Date)
	recipients := s.activeMemberIDsLocked(req.ChannelID, 0, 0)
	recipients = append(recipients, req.MemberID)
	return domain.EditChannelAdminResult{
		Channel:     channel,
		Previous:    previous,
		Participant: member,
		Event:       event,
		Recipients:  recipients,
		Date:        req.Date,
	}, nil
}

func (s *ChannelStore) EditChannelBanned(_ context.Context, req domain.EditChannelBannedRequest) (domain.EditChannelBannedResult, error) {
	if req.Participant.Type == domain.PeerTypeChannel {
		return s.editChannelSenderBan(req)
	}
	if req.UserID == 0 || req.ChannelID == 0 || req.Participant.Type != domain.PeerTypeUser || req.Participant.ID == 0 {
		return domain.EditChannelBannedResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	actor := s.members[req.ChannelID][req.UserID]
	if !canBanChannelUsers(actor) {
		return domain.EditChannelBannedResult{}, domain.ErrChannelAdminRequired
	}
	previous, ok := s.effectiveMemberLocked(req.ChannelID, req.Participant.ID, restrictionNow(req.Date))
	if !ok {
		previous = domain.ChannelMember{ChannelID: req.ChannelID, UserID: req.Participant.ID, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberLeft}
	}
	if previous.Role == domain.ChannelRoleCreator {
		return domain.EditChannelBannedResult{}, domain.ErrChannelUserCreator
	}
	if err := domain.CheckManageChannelAdmin(actor, previous); err != nil {
		return domain.EditChannelBannedResult{}, err
	}
	member := previous
	member.Role = domain.ChannelRoleMember
	member.BannedRights = req.BannedRights
	switch {
	case req.BannedRights.ViewMessages:
		member.InviterUserID = req.UserID
		member.Status = domain.ChannelMemberKicked
		member.LeftAt = req.Date
	case zeroChannelBannedRights(req.BannedRights):
		if previous.Status == domain.ChannelMemberActive {
			member.Status = domain.ChannelMemberActive
		} else {
			member.Status = domain.ChannelMemberLeft
		}
		member.LeftAt = 0
	default:
		member.InviterUserID = req.UserID
		if previous.Status == domain.ChannelMemberActive {
			member.Status = domain.ChannelMemberActive
		} else {
			member.Status = domain.ChannelMemberBanned
		}
	}
	if member.JoinedAt == 0 && member.Status == domain.ChannelMemberActive {
		member.JoinedAt = req.Date
	}
	s.members[req.ChannelID][req.Participant.ID] = member
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID:       req.ChannelID,
		UserID:          req.UserID,
		Date:            req.Date,
		Type:            adminLogBanType(previous, member),
		PrevParticipant: ptrChannelMember(previous),
		NewParticipant:  ptrChannelMember(member),
	})
	s.refreshChannelCountsLocked(req.ChannelID)
	channel = s.channels[req.ChannelID]
	event := transientChannelParticipantEvent(channel.ID, req.UserID, previous, member, req.Date)
	if member.Status == domain.ChannelMemberActive {
		if msg, ok := s.findMessageLocked(req.ChannelID, channel.TopMessageID); ok {
			s.upsertChannelDialogLocked(member.UserID, channel, msg, false)
		}
	}
	if member.Status == domain.ChannelMemberKicked && previous.Status == domain.ChannelMemberActive {
		s.clearChannelMentionsForUserLocked(req.ChannelID, req.Participant.ID)
	}
	var serviceMsg domain.ChannelMessage
	var serviceEvent domain.ChannelUpdateEvent
	if channel.Megagroup && previous.Status == domain.ChannelMemberActive && member.Status == domain.ChannelMemberKicked {
		serviceMsg, serviceEvent = s.appendChannelServiceMessageLocked(req.ChannelID, req.UserID, req.Date, domain.ChannelMessageAction{
			Type:    domain.ChannelActionChatDelete,
			UserIDs: []int64{req.Participant.ID},
		})
		channel.TopMessageID = serviceMsg.ID
		channel.Pts = serviceEvent.Pts
		s.channels[req.ChannelID] = channel
	}
	recipients := s.activeMemberIDsLocked(req.ChannelID, 0, 0)
	recipients = append(recipients, req.Participant.ID)
	return domain.EditChannelBannedResult{
		Channel:      channel,
		Previous:     previous,
		Participant:  member,
		Event:        event,
		Recipients:   recipients,
		Date:         req.Date,
		Message:      cloneChannelMessage(serviceMsg),
		ServiceEvent: cloneChannelEvent(serviceEvent),
	}, nil
}

func (s *ChannelStore) EditChannelDefaultBannedRights(_ context.Context, req domain.EditChannelDefaultBannedRightsRequest) (domain.Channel, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.Channel{}, err
	}
	actor := s.members[req.ChannelID][req.UserID]
	if !canBanChannelUsers(actor) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if channel.DefaultBannedRights == req.BannedRights {
		return domain.Channel{}, domain.ErrChannelNotModified
	}
	channel.DefaultBannedRights = req.BannedRights
	s.channels[req.ChannelID] = channel
	return channel, nil
}

func (s *ChannelStore) ListAdminedPublicChannels(_ context.Context, userID int64) ([]domain.Channel, error) {
	if userID == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Channel, 0)
	for channelID, members := range s.members {
		member := members[userID]
		if member.Status != domain.ChannelMemberActive || !isChannelAdmin(member) {
			continue
		}
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted || channel.Username == "" {
			continue
		}
		out = append(out, channel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > domain.MaxAdminedPublicChannels {
		out = out[:domain.MaxAdminedPublicChannels]
	}
	return append([]domain.Channel(nil), out...), nil
}

func (s *ChannelStore) ListCommunityLinkableChannels(_ context.Context, userID int64) ([]domain.Channel, error) {
	if userID == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Channel, 0)
	for channelID, members := range s.members {
		member := members[userID]
		if member.Status != domain.ChannelMemberActive || !isChannelAdmin(member) {
			continue
		}
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted || channel.Monoforum || channel.LinkedCommunityID != 0 {
			continue
		}
		out = append(out, channel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > domain.MaxCommunityPeers {
		out = out[:domain.MaxCommunityPeers]
	}
	return append([]domain.Channel(nil), out...), nil
}

func (s *ChannelStore) ListStoryPostableChannels(_ context.Context, userID int64) ([]domain.Channel, error) {
	if userID == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Channel, 0)
	for channelID, members := range s.members {
		member := members[userID]
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		if member.Role != domain.ChannelRoleCreator && !(member.Role == domain.ChannelRoleAdmin && member.AdminRights.PostStories) {
			continue
		}
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted || (!channel.Broadcast && !channel.Megagroup) {
			continue
		}
		out = append(out, channel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > domain.MaxStorySendAsChannels {
		out = out[:domain.MaxStorySendAsChannels]
	}
	return append([]domain.Channel(nil), out...), nil
}

func (s *ChannelStore) ListSendAsChannels(_ context.Context, userID int64) ([]domain.Channel, error) {
	if userID == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Channel, 0)
	for channelID, members := range s.members {
		member := members[userID]
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		if member.Role != domain.ChannelRoleCreator && !(member.Role == domain.ChannelRoleAdmin && member.AdminRights.PostMessages) {
			continue
		}
		channel, ok := s.channels[channelID]
		if !ok || channel.Deleted || !channel.Broadcast {
			continue
		}
		out = append(out, channel)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > domain.MaxSendAsChannels {
		out = out[:domain.MaxSendAsChannels]
	}
	return append([]domain.Channel(nil), out...), nil
}

func (s *ChannelStore) SetParticipantsHidden(_ context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !channel.Megagroup || !canBanChannelUsers(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	channel.ParticipantsHidden = enabled
	s.channels[channelID] = channel
	return channel, nil
}

func (s *ChannelStore) SetJoinToSend(_ context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !channel.Megagroup || !canExportChannelInvite(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	channel.JoinToSend = enabled
	s.channels[channelID] = channel
	return channel, nil
}

func (s *ChannelStore) SetJoinRequest(_ context.Context, userID, channelID int64, enabled bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !channel.Megagroup || !canExportChannelInvite(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if enabled && strings.TrimSpace(channel.Username) == "" {
		return domain.Channel{}, domain.ErrChatPublicRequired
	}
	channel.JoinRequest = enabled
	s.channels[channelID] = channel
	return channel, nil
}

func (s *ChannelStore) HideChatJoinRequest(_ context.Context, req domain.HideChannelJoinRequestRequest) (domain.CreateChannelResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.TargetUserID == 0 {
		return domain.CreateChannelResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.CreateChannelResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canExportChannelInvite(member) {
		return domain.CreateChannelResult{}, domain.ErrChannelAdminRequired
	}
	importer, ok := s.importers[req.ChannelID][req.TargetUserID]
	if !ok || !importer.Requested {
		return domain.CreateChannelResult{}, domain.ErrHideRequesterMissing
	}
	invite := domain.ChannelInvite{ChannelID: req.ChannelID, AdminUserID: req.UserID}
	if importer.InviteID != 0 {
		var err error
		invite, err = s.inviteByIDLocked(req.ChannelID, importer.InviteID)
		if err != nil {
			return domain.CreateChannelResult{}, err
		}
	}
	if !req.Approved {
		s.deletePendingInviteImporterLocked(invite, req.TargetUserID)
		return domain.CreateChannelResult{Channel: channel, Recipients: s.activeMemberIDsLocked(req.ChannelID, req.TargetUserID, 0)}, nil
	}
	return s.approveInviteImporterLocked(channel, invite, req.TargetUserID, req.UserID, req.Date)
}

func (s *ChannelStore) recordPublicJoinRequestLocked(channel domain.Channel, userID int64, date int) error {
	if existing, ok := s.effectiveMemberLocked(channel.ID, userID, restrictionNow(date)); ok {
		if existing.Status == domain.ChannelMemberActive {
			return domain.ErrUserAlreadyParticipant
		}
		if existing.IsKicked() {
			return domain.ErrInviteHashInvalid
		}
	}
	if s.importers[channel.ID] == nil {
		s.importers[channel.ID] = make(map[int64]domain.ChannelInviteImporter)
	}
	if existing, ok := s.importers[channel.ID][userID]; ok && existing.Requested {
		return domain.ErrInviteRequestSent
	}
	s.importers[channel.ID][userID] = domain.ChannelInviteImporter{
		ChannelID: channel.ID,
		UserID:    userID,
		Date:      date,
		Requested: true,
	}
	return nil
}

func (s *ChannelStore) ListActiveChannelMemberIDs(_ context.Context, viewerUserID, channelID int64, limit int) ([]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, _, err := s.channelAndMemberOrLinkedGuestLocked(viewerUserID, channelID); err != nil {
		return nil, err
	}
	return s.activeMemberIDsLocked(channelID, 0, limit), nil
}

func (s *ChannelStore) FilterActiveChannelMemberIDs(_ context.Context, channelID int64, userIDs []int64) ([]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if channelID == 0 || len(userIDs) == 0 {
		return nil, nil
	}
	members := s.members[channelID]
	if len(members) == 0 {
		return nil, nil
	}
	out := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		member, ok := members[userID]
		if !ok || member.Status != domain.ChannelMemberActive {
			continue
		}
		out = append(out, userID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *ChannelStore) FilterActiveChannelMemberPairs(_ context.Context, userIDsByChannel map[int64][]int64) (map[int64][]int64, error) {
	requested := make(map[int64][]int64)
	seen := make(map[[2]int64]struct{})
	for channelID, userIDs := range userIDsByChannel {
		if channelID == 0 {
			continue
		}
		for _, userID := range userIDs {
			if userID == 0 {
				continue
			}
			pair := [2]int64{channelID, userID}
			if _, ok := seen[pair]; ok {
				continue
			}
			if len(seen) >= store.MaxActiveChannelMemberPairs {
				return nil, fmt.Errorf("%w: maximum %d", store.ErrActiveChannelMemberPairsLimit, store.MaxActiveChannelMemberPairs)
			}
			seen[pair] = struct{}{}
			requested[channelID] = append(requested[channelID], userID)
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int64][]int64, len(requested))
	for channelID, userIDs := range requested {
		members := s.members[channelID]
		for _, userID := range userIDs {
			member, ok := members[userID]
			if ok && member.Status == domain.ChannelMemberActive {
				out[channelID] = append(out[channelID], userID)
			}
		}
		sort.Slice(out[channelID], func(i, j int) bool { return out[channelID][i] < out[channelID][j] })
	}
	return out, nil
}

func (s *ChannelStore) FilterChannelMessageAudienceIDs(_ context.Context, channelID int64, userIDs []int64) ([]int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if channelID == 0 || len(userIDs) == 0 {
		return nil, nil
	}
	channel, ok := s.channels[channelID]
	if !ok || channel.Deleted {
		return nil, nil
	}
	public := s.publicPreviewableChannelLocked(channel)
	now := restrictionNow(0)
	out := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		member, found := s.effectiveMemberLocked(channelID, userID, now)
		if member.IsKicked() {
			continue
		}
		// banned 是"不在群里但带着限制"：对公开频道与 left 一样能看到消息。
		if member.Status == domain.ChannelMemberActive || public && (!found || member.Status == domain.ChannelMemberLeft || member.Status == domain.ChannelMemberBanned) {
			out = append(out, userID)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

func (s *ChannelStore) ListActiveChannelMembers(_ context.Context, viewerUserID, channelID int64, limit int) (domain.Channel, domain.ChannelMember, []domain.ChannelMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, viewer, _, err := s.channelForViewerLocked(viewerUserID, channelID)
	if err != nil {
		return domain.Channel{}, domain.ChannelMember{}, nil, err
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	members := s.members[channelID]
	out := make([]domain.ChannelMember, 0, minInt(limit, len(members)))
	for _, member := range members {
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		out = append(out, member)
		if len(out) >= limit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return channel, viewer, cloneChannelMembers(out), nil
}

func (s *ChannelStore) channelForMemberLocked(userID, channelID int64) (domain.Channel, error) {
	channel, _, err := s.channelAndMemberLocked(userID, channelID)
	return channel, err
}

func (s *ChannelStore) channelAndMemberLocked(userID, channelID int64) (domain.Channel, domain.ChannelMember, error) {
	channel, ok := s.channels[channelID]
	if !ok || channel.Deleted {
		return domain.Channel{}, domain.ChannelMember{}, domain.ErrChannelInvalid
	}
	member, ok := s.effectiveMemberLocked(channelID, userID, restrictionNow(0))
	// banned 是"不在群里但带着限制"，与 left 一样不是成员；只有踢出才是 ErrChannelUserBanned。
	if !ok || member.Status == domain.ChannelMemberLeft || member.Status == domain.ChannelMemberBanned {
		return domain.Channel{}, domain.ChannelMember{}, domain.ErrChannelPrivate
	}
	if member.IsKicked() {
		return domain.Channel{}, domain.ChannelMember{}, domain.ErrChannelUserBanned
	}
	return channel, member, nil
}

func (s *ChannelStore) channelAndMemberOrLinkedGuestLocked(userID, channelID int64) (domain.Channel, domain.ChannelMember, error) {
	channel, member, err := s.channelAndMemberLocked(userID, channelID)
	if !errors.Is(err, domain.ErrChannelPrivate) {
		return channel, member, err
	}
	target, ok := s.channels[channelID]
	if !ok || target.Deleted {
		return domain.Channel{}, domain.ChannelMember{}, domain.ErrChannelInvalid
	}
	guest, allowed, guestErr := s.linkedDiscussionGuestLocked(userID, target)
	if guestErr != nil {
		return domain.Channel{}, domain.ChannelMember{}, guestErr
	}
	if !allowed {
		return domain.Channel{}, domain.ChannelMember{}, err
	}
	return target, guest, nil
}

// linkedDiscussionGuestLocked authorizes a discussion-group view through the
// viewer's active membership in its bidirectionally linked broadcast. It never
// materializes a channel_members row. Explicit target bans always win.
func (s *ChannelStore) linkedDiscussionGuestLocked(userID int64, target domain.Channel) (domain.ChannelMember, bool, error) {
	if target.Broadcast || !target.Megagroup || target.LinkedChatID == 0 {
		return domain.ChannelMember{}, false, nil
	}
	if existing, ok := s.effectiveMemberLocked(target.ID, userID, restrictionNow(0)); ok {
		if existing.IsKicked() {
			return domain.ChannelMember{}, false, domain.ErrChannelUserBanned
		}
		if existing.Status == domain.ChannelMemberActive {
			return existing, false, nil
		}
	}
	source, ok := s.channels[target.LinkedChatID]
	if !ok || source.Deleted || !source.Broadcast || source.LinkedChatID != target.ID {
		return domain.ChannelMember{}, false, nil
	}
	sourceMember, ok := s.effectiveMemberLocked(source.ID, userID, restrictionNow(0))
	if !ok || sourceMember.Status != domain.ChannelMemberActive || sourceMember.BannedRights.ViewMessages {
		return domain.ChannelMember{}, false, nil
	}
	return domain.ChannelMember{
		ChannelID: target.ID,
		UserID:    userID,
		Status:    domain.ChannelMemberLeft,
		Role:      domain.ChannelRoleMember,
		Guest:     true,
	}, true, nil
}

func (s *ChannelStore) activeMemberIDsLocked(channelID, excludeUserID int64, limit int) []int64 {
	members := s.members[channelID]
	if limit <= 0 || limit > domain.MaxChannelRealtimeFanout {
		limit = domain.MaxChannelRealtimeFanout
	}
	capacity := limit
	if len(members) < capacity {
		capacity = len(members)
	}
	out := make([]int64, 0, capacity)
	for userID, member := range members {
		if userID == excludeUserID || member.Status != domain.ChannelMemberActive {
			continue
		}
		out = append(out, userID)
		if len(out) >= limit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *ChannelStore) futureCreatorAfterLeaveLocked(channelID, userID int64) (domain.ChannelMember, error) {
	members := s.members[channelID]
	var selected domain.ChannelMember
	found := false
	for _, member := range members {
		if member.UserID == userID || member.Status != domain.ChannelMemberActive || member.Role == domain.ChannelRoleCreator || member.BannedRights.ViewMessages {
			continue
		}
		if !found || futureCreatorCandidateLess(member, selected) {
			selected = member
			found = true
		}
	}
	if !found {
		return domain.ChannelMember{}, domain.ErrUserNotParticipant
	}
	return selected, nil
}

func futureCreatorCandidateLess(a, b domain.ChannelMember) bool {
	if channelRoleOrder(a.Role) != channelRoleOrder(b.Role) {
		return channelRoleOrder(a.Role) < channelRoleOrder(b.Role)
	}
	return a.UserID < b.UserID
}

func publicPreviewMember(channel domain.Channel, userID int64, existing domain.ChannelMember, found bool) domain.ChannelMember {
	member := domain.ChannelMember{
		ChannelID:       channel.ID,
		UserID:          userID,
		Role:            domain.ChannelRoleMember,
		Status:          domain.ChannelMemberLeft,
		AvailableMinID:  channelInitialAvailableMinID(channel),
		AvailableMinPts: channelInitialAvailableMinPts(channel),
		ReadInboxMaxID:  channel.TopMessageID,
		ReadOutboxMaxID: channel.TopMessageID,
	}
	if found {
		member.InviterUserID = existing.InviterUserID
		member.JoinedAt = existing.JoinedAt
		member.LeftAt = existing.LeftAt
		member.AvailableMinID = maxInt(member.AvailableMinID, existing.AvailableMinID)
		member.AvailableMinPts = maxInt(member.AvailableMinPts, existing.AvailableMinPts)
		member.ReadInboxMaxID = maxInt(member.ReadInboxMaxID, existing.ReadInboxMaxID)
		member.ReadOutboxMaxID = maxInt(member.ReadOutboxMaxID, existing.ReadOutboxMaxID)
	}
	return member
}

func syntheticMonoforumAdminMember(mono domain.Channel, parentMember domain.ChannelMember) domain.ChannelMember {
	member := parentMember
	member.ChannelID = mono.ID
	member.Status = domain.ChannelMemberActive
	if mono.CreatorUserID == parentMember.UserID {
		member.Role = domain.ChannelRoleCreator
	} else {
		member.Role = domain.ChannelRoleAdmin
	}
	member.AvailableMinID = 0
	member.AvailableMinPts = 0
	member.HistoryClearAnchorID = 0
	member.HistoryClearAnchorDate = 0
	member.ReadInboxMaxID = mono.TopMessageID
	member.ReadOutboxMaxID = mono.TopMessageID
	member.UnreadMark = false
	member.SlowmodeLastSendDate = 0
	return member
}

func syntheticMonoforumUserMember(mono domain.Channel, userID int64) domain.ChannelMember {
	return domain.ChannelMember{
		ChannelID: mono.ID,
		UserID:    userID,
		Role:      domain.ChannelRoleMember,
		Status:    domain.ChannelMemberActive,
	}
}

func publicChannelSearchRank(channel domain.Channel, queryLower string) (int, bool) {
	if !publicSearchableChannel(channel) {
		return 0, false
	}
	username := strings.ToLower(strings.TrimSpace(channel.Username))
	title := strings.ToLower(strings.TrimSpace(channel.Title))
	switch {
	case username == queryLower:
		return 0, true
	case strings.HasPrefix(username, queryLower):
		return 1, true
	case strings.Contains(username, queryLower):
		return 2, true
	case strings.HasPrefix(title, queryLower):
		return 3, true
	case strings.Contains(title, queryLower):
		return 4, true
	default:
		return 0, false
	}
}

func canPostToBroadcast(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.PostMessages)
}

func isChannelAdmin(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin
}

func shouldHideAnonymousAdminFromParticipantList(viewer, member domain.ChannelMember) bool {
	if isChannelAdmin(viewer) {
		return false
	}
	if member.Role != domain.ChannelRoleCreator && member.Role != domain.ChannelRoleAdmin {
		return false
	}
	return member.AdminRights.Anonymous
}

func channelParticipantMatchesFilter(member domain.ChannelMember, kind domain.ChannelParticipantsFilterKind, query string) bool {
	if query != "" && !strings.Contains(strconv.FormatInt(member.UserID, 10), query) {
		return false
	}
	switch kind {
	case "", domain.ChannelParticipantsRecent, domain.ChannelParticipantsContacts, domain.ChannelParticipantsMentions, domain.ChannelParticipantsSearch:
		return member.Status == domain.ChannelMemberActive
	case domain.ChannelParticipantsAdmins:
		// Layer 225 起 admins filter 同时是消息徽章数据源：客户端把整个返回
		// （含 rank）灌进 badge 缓存，因此带成员 Tag 的普通成员也必须返回。
		return member.Status == domain.ChannelMemberActive && (isChannelAdmin(member) || member.Rank != "")
	case domain.ChannelParticipantsKicked:
		return member.Status == domain.ChannelMemberKicked || member.BannedRights.ViewMessages
	case domain.ChannelParticipantsBanned:
		return member.Status != domain.ChannelMemberKicked && !zeroChannelBannedRights(member.BannedRights)
	case domain.ChannelParticipantsBots:
		return false
	default:
		return member.Status == domain.ChannelMemberActive
	}
}

func canManageDiscussionBroadcast(member domain.ChannelMember) bool {
	return canChangeChannelInfo(member)
}

func canManageDiscussionGroup(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.PinMessages)
}

func canAddChannelAdmins(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.AddAdmins)
}

func canBanChannelUsers(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.BanUsers)
}

func zeroChannelAdminRights(rights domain.ChannelAdminRights) bool {
	return rights == domain.ChannelAdminRights{}
}

func zeroChannelBannedRights(rights domain.ChannelBannedRights) bool {
	return rights == domain.ChannelBannedRights{}
}

// rejoinBannedRights 返回旧成员记录重新入群后应带的限制：退群不解除限制，
// 否则"先退群再让人拉回来"就能绕过；不在群里时被限制的人（Status banned）同样带着限制入群。
// 被踢出者（IsKicked）能重新入群只可能是被有封禁权的管理员拉回，等同解封。
// 期限（UntilDate，0 为永久）在入群时刻 date 已过的限制视为无限制。
func rejoinBannedRights(existing domain.ChannelMember, date int) domain.ChannelBannedRights {
	existing = existing.EffectiveAt(restrictionNow(date))
	if existing.IsKicked() {
		return domain.ChannelBannedRights{}
	}
	return existing.BannedRights
}

func creatorChannelAdminRights() domain.ChannelAdminRights {
	return domain.CreatorChannelAdminRights()
}

func cloneChannelMembers(in []domain.ChannelMember) []domain.ChannelMember {
	return append([]domain.ChannelMember(nil), in...)
}

func ptrChannelMember(in domain.ChannelMember) *domain.ChannelMember {
	out := in
	return &out
}
