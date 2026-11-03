package channels

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"telesrv/internal/domain"
)

// BotProfileResolver is the domain-only view of bot metadata used by channel policy.
type BotProfileResolver interface {
	BotInfo(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error)
}

type botProfileBatchResolver interface {
	BotInfos(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error)
}

type activeChannelBotMemberLister interface {
	ListActiveChannelBotMembers(ctx context.Context, viewerUserID, channelID int64, offset, limit int) (domain.ChannelParticipantList, error)
}

type activeChannelBotMemberRoleLister interface {
	ListActiveChannelBotMemberRoles(ctx context.Context, viewerUserID, channelID int64, limit int) ([]domain.ChannelBotMember, error)
}

// botUsernameResolver 是 BotProfileResolver 的可选能力：取 bot 当前用户名，判定 /cmd@username
// 是否发给它。没有该能力时带 @ 后缀的命令按发给所有 bot 处理。
type botUsernameResolver interface {
	BotUsername(ctx context.Context, botUserID int64) (string, bool, error)
}

func (s *Service) getBotParticipants(ctx context.Context, userID, channelID int64, offset, limit int) (domain.ChannelParticipantList, error) {
	if lister, ok := s.channels.(activeChannelBotMemberLister); ok {
		return lister.ListActiveChannelBotMembers(ctx, userID, channelID, offset, limit)
	}
	channel, viewer, active, err := s.channels.ListActiveChannelMembers(ctx, userID, channelID, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	if channel.ParticipantsHidden && !channelServiceMemberIsAdmin(viewer) {
		return domain.ChannelParticipantList{Channel: channel, Count: 0}, nil
	}
	if offset < 0 {
		offset = 0
	}
	if offset > domain.MaxChannelParticipantsOffset {
		offset = domain.MaxChannelParticipantsOffset
	}
	ids := make([]int64, 0, len(active))
	for _, member := range active {
		if member.UserID != 0 {
			ids = append(ids, member.UserID)
		}
	}
	profiles, err := s.botProfiles(ctx, ids)
	if err != nil {
		return domain.ChannelParticipantList{}, err
	}
	members := make([]domain.ChannelMember, 0, limit)
	count := 0
	for _, member := range active {
		if _, found := profiles[member.UserID]; !found {
			continue
		}
		if count >= offset && len(members) < limit {
			members = append(members, member)
		}
		count++
	}
	return domain.ChannelParticipantList{Channel: channel, Participants: members, Count: count}, nil
}

func (s *Service) botProfiles(ctx context.Context, ids []int64) (map[int64]domain.BotProfile, error) {
	if len(ids) == 0 || s.bots == nil {
		return nil, nil
	}
	if batch, ok := s.bots.(botProfileBatchResolver); ok {
		return batch.BotInfos(ctx, ids)
	}
	out := make(map[int64]domain.BotProfile)
	for _, id := range uniqueNonZero(ids) {
		profile, found, err := s.bots.BotInfo(ctx, id)
		if err != nil {
			return nil, err
		}
		if found {
			out[id] = profile
		}
	}
	return out, nil
}

func (s *Service) rejectBlockedBotInvites(ctx context.Context, userIDs []int64) error {
	if s.bots == nil {
		return nil
	}
	for _, id := range uniqueNonZero(userIDs) {
		profile, found, err := s.bots.BotInfo(ctx, id)
		if err != nil {
			return err
		}
		if found && profile.Nochats {
			return domain.ErrBotGroupsBlocked
		}
	}
	return nil
}

func (s *Service) skippedBotDeliveryUserIDs(ctx context.Context, req domain.SendChannelMessageRequest) ([]int64, error) {
	if s.bots == nil || req.ChannelID == 0 || req.UserID == 0 {
		return nil, nil
	}
	members, err := s.loadActiveBotMembers(ctx, req.UserID, req.ChannelID, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		return nil, err
	}
	return s.skippedBotDeliveryUserIDsForMembers(ctx, req, members)
}

// ActiveBotMemberIDs returns active bot members for non-privacy-critical producers
// such as Bot API update queue fanout. Privacy delivery decisions use
// loadActiveBotMemberIDs directly to avoid stale-cache leaks.
func (s *Service) ActiveBotMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error) {
	if s == nil || s.channels == nil || viewerUserID == 0 || channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	key := activeBotMemberIDsCacheKey{viewerUserID: viewerUserID, channelID: channelID, limit: limit}
	if s.versions != nil {
		hash, err := s.channelBotMemberIDsHash(ctx, viewerUserID, channelID, key)
		if err != nil {
			return nil, err
		}
		if hash != 0 {
			return s.botMemberIDsCache.getOrLoadVersioned(ctx, key, hash, func() ([]int64, error) {
				return s.loadActiveBotMemberIDs(ctx, viewerUserID, channelID, limit)
			})
		}
		return s.loadActiveBotMemberIDs(ctx, viewerUserID, channelID, limit)
	}
	return s.botMemberIDsCache.getOrLoad(ctx, key, func() ([]int64, error) {
		return s.loadActiveBotMemberIDs(ctx, viewerUserID, channelID, limit)
	})
}

func (s *Service) loadActiveBotMemberIDs(ctx context.Context, viewerUserID, channelID int64, limit int) ([]int64, error) {
	if s == nil || s.channels == nil || viewerUserID == 0 || channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	if lister, ok := s.channels.(activeChannelBotMemberRoleLister); ok {
		members, err := lister.ListActiveChannelBotMemberRoles(ctx, viewerUserID, channelID, limit)
		if err != nil {
			return nil, err
		}
		return botMemberIDs(members), nil
	}
	if s.bots == nil {
		return nil, nil
	}
	memberIDs, err := s.channels.ListActiveChannelMemberIDs(ctx, viewerUserID, channelID, limit)
	if err != nil {
		return nil, err
	}
	profiles, err := s.botProfiles(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(profiles))
	for _, id := range uniqueNonZero(memberIDs) {
		if _, found := profiles[id]; found {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// loadActiveBotMembers 列出群里在群的 bot 成员及其角色，供 privacy 投递判定；不走缓存，
// 角色变更（任免管理员）立刻生效，避免陈旧缓存把消息漏给刚被撤权的 bot。
func (s *Service) loadActiveBotMembers(ctx context.Context, viewerUserID, channelID int64, limit int) ([]domain.ChannelBotMember, error) {
	if s == nil || s.channels == nil || viewerUserID == 0 || channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 || limit > domain.MaxSynchronousChannelDialogFanout {
		limit = domain.MaxSynchronousChannelDialogFanout
	}
	if lister, ok := s.channels.(activeChannelBotMemberRoleLister); ok {
		return lister.ListActiveChannelBotMemberRoles(ctx, viewerUserID, channelID, limit)
	}
	if s.bots == nil {
		return nil, nil
	}
	_, _, active, err := s.channels.ListActiveChannelMembers(ctx, viewerUserID, channelID, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(active))
	for _, member := range active {
		ids = append(ids, member.UserID)
	}
	profiles, err := s.botProfiles(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ChannelBotMember, 0, len(profiles))
	for _, member := range active {
		if _, found := profiles[member.UserID]; found && member.UserID != 0 {
			out = append(out, domain.ChannelBotMember{UserID: member.UserID, Role: member.Role})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

func botMemberIDs(members []domain.ChannelBotMember) []int64 {
	out := make([]int64, 0, len(members))
	for _, member := range members {
		out = append(out, member.UserID)
	}
	return out
}

func (s *Service) skippedBotDeliveryUserIDsForMembers(ctx context.Context, req domain.SendChannelMessageRequest, members []domain.ChannelBotMember) ([]int64, error) {
	msg := domain.ChannelMessage{
		ChannelID:    req.ChannelID,
		SenderUserID: req.UserID,
		Body:         req.Message,
		ReplyTo:      req.ReplyTo,
		ViaBotID:     req.ViaBotID,
		Action:       req.Action,
	}
	return s.skippedPrivacyBotIDs(ctx, req.UserID, members, []domain.ChannelMessage{msg}, req.MentionUserIDs)
}

// skippedPrivacyBotIDs 是新消息与编辑推送共用的 privacy bot 跳过规则：members 中开着 privacy mode
// （bot_chat_history=false）且不是群管理员的 bot，若 msgs 里没有一条对它可见，就跳过直接推送。
// actorUserID（发送者/编辑者）本人永不跳过。
func (s *Service) skippedPrivacyBotIDs(ctx context.Context, actorUserID int64, members []domain.ChannelBotMember, msgs []domain.ChannelMessage, mentionUserIDs []int64) ([]int64, error) {
	profiles, err := s.botProfiles(ctx, botMemberIDs(members))
	if err != nil {
		return nil, err
	}
	skip := make([]int64, 0)
	for _, member := range members {
		id := member.UserID
		if id == actorUserID {
			continue
		}
		profile, found := profiles[id]
		if !found {
			continue
		}
		viewer := &botViewer{userID: id, exempt: botPrivacyExempt(profile, member.Role)}
		visible := false
		for _, msg := range msgs {
			if s.botCanSeeChannelMessage(ctx, viewer, msg, mentionUserIDs) {
				visible = true
				break
			}
		}
		if !visible {
			skip = append(skip, id)
		}
	}
	return skip, nil
}

// applyEditPrivacyBotSkip 按新消息同一套规则给编辑结果填 SkipDeliveryUserIDs 并从 Recipients 扣除，
// 使在线编辑 fanout 不把 privacy bot 看不见的消息编辑推给它（getChannelDifference 已按同规则过滤）。
// 可见性按编辑后的消息判断；todo 编辑的服务消息容器只要有一条可见即推送整条更新。
func (s *Service) applyEditPrivacyBotSkip(ctx context.Context, actorUserID int64, mentionUserIDs []int64, res domain.EditChannelMessageResult) (domain.EditChannelMessageResult, error) {
	if s.bots == nil || res.Channel.ID == 0 || actorUserID == 0 {
		return res, nil
	}
	msgs := make([]domain.ChannelMessage, 0, 2)
	if res.Message.ID != 0 {
		msgs = append(msgs, res.Message)
	}
	if res.ServiceEvent.Pts != 0 && res.ServiceMessage.ID != 0 {
		msgs = append(msgs, res.ServiceMessage)
	}
	if len(msgs) == 0 {
		return res, nil
	}
	members, err := s.loadActiveBotMembers(ctx, actorUserID, res.Channel.ID, domain.MaxSynchronousChannelDialogFanout)
	if err != nil {
		return domain.EditChannelMessageResult{}, err
	}
	skip, err := s.skippedPrivacyBotIDs(ctx, actorUserID, members, msgs, mentionUserIDs)
	if err != nil {
		return domain.EditChannelMessageResult{}, err
	}
	if len(skip) == 0 {
		return res, nil
	}
	res.SkipDeliveryUserIDs = mergeSkippedUserIDs(res.SkipDeliveryUserIDs, skip)
	recipients := make([]int64, 0, len(res.Recipients))
	for _, id := range res.Recipients {
		if !containsInt64(res.SkipDeliveryUserIDs, id) {
			recipients = append(recipients, id)
		}
	}
	res.Recipients = recipients
	return res, nil
}

func (s *Service) filterBotChannelHistory(ctx context.Context, userID int64, history domain.ChannelHistory) domain.ChannelHistory {
	if s.bots == nil || userID == 0 || history.Channel.ID == 0 {
		return history
	}
	profile, found, err := s.bots.BotInfo(ctx, userID)
	if err != nil || !found || botPrivacyExempt(profile, history.Self.Role) {
		return history
	}
	viewer := &botViewer{userID: userID}
	filtered := history
	filtered.Messages = make([]domain.ChannelMessage, 0, len(history.Messages))
	for _, msg := range history.Messages {
		if s.botCanSeeChannelMessage(ctx, viewer, msg, nil) {
			filtered.Messages = append(filtered.Messages, msg)
		}
	}
	filtered.Count = len(filtered.Messages)
	filtered.Users = nil
	filtered.Channels = nil
	return filtered
}

func (s *Service) filterBotChannelDifference(ctx context.Context, userID int64, diff domain.ChannelDifference) domain.ChannelDifference {
	if s.bots == nil || userID == 0 || diff.Channel.ID == 0 {
		return diff
	}
	profile, found, err := s.bots.BotInfo(ctx, userID)
	if err != nil || !found || botPrivacyExempt(profile, diff.Self.Role) {
		return diff
	}
	viewer := &botViewer{userID: userID}
	filtered := diff
	filtered.NewMessages = nil
	filtered.OtherUpdates = nil
	filtered.Events = nil
	filtered.Users = nil
	filtered.Channels = nil
	if diff.TooLong {
		for _, msg := range diff.NewMessages {
			if s.botCanSeeChannelMessage(ctx, viewer, msg, nil) {
				filtered.NewMessages = append(filtered.NewMessages, msg)
			}
		}
		return filtered
	}
	for _, event := range diff.Events {
		visibleEvent, ok := s.filterBotChannelEvent(ctx, viewer, event)
		if !ok {
			continue
		}
		filtered.Events = append(filtered.Events, visibleEvent)
		switch visibleEvent.Type {
		case domain.ChannelUpdateNewMessage:
			filtered.NewMessages = append(filtered.NewMessages, visibleEvent.Message)
		default:
			filtered.OtherUpdates = append(filtered.OtherUpdates, visibleEvent)
		}
	}
	return filtered
}

func (s *Service) filterBotChannelEvent(ctx context.Context, viewer *botViewer, event domain.ChannelUpdateEvent) (domain.ChannelUpdateEvent, bool) {
	switch event.Type {
	case domain.ChannelUpdateNewMessage, domain.ChannelUpdateEditMessage:
		if event.Message.ID == 0 {
			return event, true
		}
		if !s.botCanSeeChannelMessage(ctx, viewer, event.Message, nil) {
			return domain.ChannelUpdateEvent{}, false
		}
		return event, true
	case domain.ChannelUpdateDeleteMessages:
		// 删除事件只携带消息 id、不含任何内容,且在线推送路径(channels_updates 的
		// channelDeleteMessagesUpdates→enqueueChannelFanout)本就对全体成员无差别投递删除。
		// 若在此按可见性过滤,会因被删消息无法重取(GetChannelMessages 带 AND NOT deleted
		// 恒返空)而把整条 delete 事件丢弃——privacy bot 经 getChannelDifference 补差时将
		// 对所有删除失明(连它本可见消息的删除也收不到),客户端缓存残留"未删"态。故直接放行,
		// 与在线推送行为一致(删除 id 不泄漏内容)。
		return event, true
	case domain.ChannelUpdatePinnedMessages:
		if len(event.MessageIDs) == 0 {
			return event, true
		}
		ids := make([]int, 0, len(event.MessageIDs))
		for _, id := range event.MessageIDs {
			history, err := s.channels.GetChannelMessages(ctx, viewer.userID, event.ChannelID, []int{id})
			if err != nil || len(history.Messages) == 0 {
				continue
			}
			if s.botCanSeeChannelMessage(ctx, viewer, history.Messages[0], nil) {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return domain.ChannelUpdateEvent{}, false
		}
		event.MessageIDs = ids
		return event, true
	default:
		return event, true
	}
}

// botPrivacyExempt 报告 bot 是否不受 privacy mode 限制：关了 privacy mode（bot_chat_history），
// 或是群的创建者/管理员。与官方一致，群管理员 bot 能看到群里全部消息。
func botPrivacyExempt(profile domain.BotProfile, role domain.ChannelMemberRole) bool {
	return profile.ChatHistory || role.IsAdmin()
}

// botViewer 是一次可见性判定里的某个 bot。exempt=true 时全部可见；用户名只在遇到
// 带 @ 后缀的命令时才查，并在本次判定内复用。
type botViewer struct {
	userID         int64
	exempt         bool
	username       string
	usernameLoaded bool
}

func (s *Service) botViewerUsername(ctx context.Context, viewer *botViewer) string {
	if !viewer.usernameLoaded {
		viewer.usernameLoaded = true
		if resolver, ok := s.bots.(botUsernameResolver); ok {
			if username, found, err := resolver.BotUsername(ctx, viewer.userID); err == nil && found {
				viewer.username = username
			}
		}
	}
	return viewer.username
}

// botCanSeeChannelMessage 判定开着 privacy mode 的 bot 能否看到一条群消息，规则对齐官方：
// 它自己发的、@它的、所有服务消息、经它 inline 发出的、发给它的命令、回复它或其 inline 消息的。
// 不带 @ 后缀的命令发给群里所有 bot（官方只发给最近发过言的 bot，暂未实现）。
func (s *Service) botCanSeeChannelMessage(ctx context.Context, viewer *botViewer, msg domain.ChannelMessage, mentionUserIDs []int64) bool {
	if viewer == nil || viewer.userID == 0 || viewer.exempt {
		return true
	}
	botUserID := viewer.userID
	if msg.SenderUserID == botUserID || msg.ViaBotID == botUserID {
		return true
	}
	if msg.Mentioned || containsInt64(mentionUserIDs, botUserID) {
		return true
	}
	if msg.Action != nil {
		return true
	}
	if isCommand, target := messageCommandTarget(msg.Body); isCommand {
		if target == "" {
			return true
		}
		// 取不到用户名时无法判定归属，按发给它处理。
		if username := s.botViewerUsername(ctx, viewer); username == "" || strings.EqualFold(username, target) {
			return true
		}
	}
	if msg.ReplyTo != nil && msg.ReplyTo.MessageID > 0 {
		history, err := s.channels.GetChannelMessages(ctx, botUserID, msg.ChannelID, []int{msg.ReplyTo.MessageID})
		if err != nil {
			return false
		}
		for _, target := range history.Messages {
			if target.SenderUserID == botUserID || target.ViaBotID == botUserID {
				return true
			}
		}
	}
	return false
}

// messageCommandTarget 解析 bot 命令：isCommand 表示消息以 /命令 开头；target 是
// /cmd@username 里的 username（不带 @），不带后缀时为空。
func messageCommandTarget(message string) (isCommand bool, target string) {
	message = strings.TrimSpace(message)
	if !strings.HasPrefix(message, "/") || len(message) == 1 {
		return false, ""
	}
	word := message[1:]
	if end := strings.IndexFunc(word, unicode.IsSpace); end >= 0 {
		word = word[:end]
	}
	if at := strings.IndexByte(word, '@'); at >= 0 {
		return true, word[at+1:]
	}
	return true, ""
}

func containsInt64(ids []int64, target int64) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func channelServiceMemberIsAdmin(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || member.Role == domain.ChannelRoleAdmin
}

func mergeSkippedUserIDs(a, b []int64) []int64 {
	out := append(append([]int64(nil), a...), b...)
	out = uniqueNonZero(out)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
