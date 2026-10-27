package memory

import "telesrv/internal/domain"

// countPostForwardLocked 给被转发的频道帖子转发数加一；不是帖子、已删除或不存在时不计。
// 调用方持有 s.mu。
func (s *ChannelStore) countPostForwardLocked(ref *domain.ChannelPostRef) {
	if ref == nil || ref.ChannelID == 0 || ref.MessageID <= 0 {
		return
	}
	idx, ok := s.findMessageIndexLocked(ref.ChannelID, ref.MessageID)
	if !ok {
		return
	}
	msg := s.messages[ref.ChannelID][idx]
	if !msg.Post || msg.Deleted {
		return
	}
	msg.ForwardsCount++
	s.messages[ref.ChannelID][idx] = msg
}

// countPostForward 是私聊写入用的入口：私聊与频道各有自己的锁，这里单独取频道锁。
func (s *ChannelStore) countPostForward(ref *domain.ChannelPostRef) {
	if s == nil || ref == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.countPostForwardLocked(ref)
}
