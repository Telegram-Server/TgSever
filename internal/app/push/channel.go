package push

import (
	"context"
	"slices"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// MemberPager 按 user_id 升序分页列出群组/频道的活跃成员（keyset：只返回 user_id > afterUserID 的，
// 最多 limit 个）。store.ChannelMemberPager 满足它。
type MemberPager interface {
	ListActiveChannelMemberIDsAfter(ctx context.Context, channelID, afterUserID int64, limit int) ([]int64, error)
}

// channelJobKind 是群组/频道推送任务的类型（队列丢弃计数的 kind）。
const channelJobKind = "channel"

// ChannelMessage 把群组（超级群）或频道里的一条新消息放进推送队列，立即返回；
// 返回 false 表示服务停用或队列已满被丢弃（计数 kind=channel）。
//
// 收件人是消息发出后的完整成员列表，不是实时分发的在线名单：连接还在、但 App 已报后台的成员也要推。
// worker 按 MemberBatch 分批读取成员：每批先把下一批放回队列（同样非阻塞，满了就丢弃剩余批次并计数；
// 关停期间不再放回），
// 再给本批成员去掉发送者、按设备判断前台与通知设置并发送。一条消息同一时刻最多占一个队列位置和
// 一批成员 id 的内存，大频道不会占满内存，也不会拖慢调用方。
// 没有注入 Members 时只推 msg.Recipients（同样分批）。
// 相册的一项（GroupedID 非 0）先按 (会话, grouped_id) 合并，整组只扇出一次（album.go）。
func (s *Service) ChannelMessage(msg NewMessage) bool {
	if !s.Enabled() {
		return false
	}
	msg.Recipients = append([]int64(nil), msg.Recipients...)
	msg.album = nil
	if msg.GroupedID != 0 && s.bufferAlbumItem(true, msg) {
		return true
	}
	return s.enqueueChannelMessage(msg)
}

func (s *Service) enqueueChannelMessage(msg NewMessage) bool {
	if s.members == nil {
		return s.enqueueRecipientBatch(msg, 0)
	}
	return s.enqueueMemberPage(msg, 0)
}

func (s *Service) enqueueMemberPage(msg NewMessage, afterUserID int64) bool {
	return s.enqueue(channelJobKind, func(ctx context.Context) { s.deliverMemberPage(ctx, msg, afterUserID) })
}

func (s *Service) deliverMemberPage(ctx context.Context, msg NewMessage, afterUserID int64) {
	batch := s.cfg.MemberBatch
	members, err := s.members.ListActiveChannelMemberIDsAfter(ctx, msg.Chat.ID, afterUserID, batch)
	if err != nil {
		s.log.Warn("list channel members for push failed", zap.Int64("channel_id", msg.Chat.ID),
			zap.Int64("after_user_id", afterUserID), zap.Error(err))
		return
	}
	if len(members) >= batch && !s.stopping.Load() {
		if last := slices.Max(members); last > afterUserID {
			s.enqueueMemberPage(msg, last)
		}
	}
	s.deliverChatBatch(ctx, msg, members)
}

func (s *Service) enqueueRecipientBatch(msg NewMessage, offset int) bool {
	return s.enqueue(channelJobKind, func(ctx context.Context) {
		end := min(offset+s.cfg.MemberBatch, len(msg.Recipients))
		if end < len(msg.Recipients) && !s.stopping.Load() {
			s.enqueueRecipientBatch(msg, end)
		}
		s.deliverChatBatch(ctx, msg, msg.Recipients[offset:end])
	})
}

// deliverChatBatch 给一批成员推送：去掉发送者，一次读出整批的设备登记（连同授权的 user），
// 一次判定整批的通知设置，文案模板按语言在本批内只查一次。
func (s *Service) deliverChatBatch(ctx context.Context, msg NewMessage, members []int64) {
	recipients := make([]int64, 0, len(members))
	for _, id := range members {
		if id != 0 && id != msg.SenderID {
			recipients = append(recipients, id)
		}
	}
	if len(recipients) == 0 {
		return
	}
	devices, err := s.devices.ListPushDevicesByUsers(ctx, recipients)
	if err != nil {
		s.log.Warn("list push devices failed", zap.Int64("channel_id", msg.Chat.ID), zap.Int("users", len(recipients)), zap.Error(err))
		return
	}
	byUser := make(map[int64][]domain.PushDevice, len(devices))
	for _, device := range devices {
		byUser[device.UserID] = append(byUser[device.UserID], device)
	}
	// 先过滤出每人要推的设备，再一次判定整批的通知设置（不逐人查询）。
	targets := make(map[int64][]domain.PushDevice, len(byUser))
	withTargets := make([]int64, 0, len(byUser))
	for _, recipient := range recipients {
		if own := s.messageTargets(recipient, byUser[recipient], msg); len(own) != 0 {
			targets[recipient] = own
			withTargets = append(withTargets, recipient)
		}
	}
	if len(withTargets) == 0 {
		return
	}
	decisions, err := s.decide(ctx, withTargets, msg.Chat)
	if err != nil {
		s.log.Warn("push notify policy failed", zap.Int64("channel_id", msg.Chat.ID), zap.Int("users", len(withTargets)), zap.Error(err))
		return
	}
	render := newJobRenderer(s.render)
	for _, recipient := range withTargets {
		s.sendMessage(ctx, channelJobKind, render, recipient, targets[recipient], msg, decisions[recipient])
	}
}

// chatFromID 是 payload 里的群组发送者（chat_from_id）：只有群组、且不是匿名作者时才带。
func (m NewMessage) chatFromID() int64 {
	if m.Chat.Kind != ChatGroup || m.AuthorName != "" {
		return 0
	}
	return m.SenderID
}
