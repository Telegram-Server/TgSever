package memory

import (
	"context"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type contactSignUpKey struct {
	owner int64
	from  int64
}

// ContactSignUpNoticeStore 是 store.ContactSignUpNoticeStore 的内存实现：在 MessageStore 的消息、
// 会话与它挂着的 UpdateEventStore 上写只属于 Owner 一侧的服务消息。账号资格查 users（在拿消息锁
// 之前查，内存实现不与注销互斥）。
type ContactSignUpNoticeStore struct {
	messages *MessageStore
	users    *UserStore
	// written 记每对 (Owner, From) 第一次写入的消息，受 messages.mu 保护。
	written map[contactSignUpKey]domain.Message
}

var _ store.ContactSignUpNoticeStore = (*ContactSignUpNoticeStore)(nil)

// NewContactSignUpNoticeStore 创建写在 messages 上的服务消息写入；messages 必须挂了 UpdateEventStore。
func NewContactSignUpNoticeStore(messages *MessageStore, users *UserStore) *ContactSignUpNoticeStore {
	return &ContactSignUpNoticeStore{messages: messages, users: users, written: make(map[contactSignUpKey]domain.Message)}
}

func (s *ContactSignUpNoticeStore) DeliverContactSignUpNotice(ctx context.Context, n domain.ContactSignUpNotice) (domain.Message, bool, error) {
	if s == nil || s.messages == nil || s.users == nil {
		return domain.Message{}, false, fmt.Errorf("memory contact sign up notice: message and user stores are required")
	}
	if err := s.checkEligible(ctx, n); err != nil {
		return domain.Message{}, false, err
	}
	if n.Date == 0 {
		n.Date = int(time.Now().Unix())
	}
	m := s.messages
	events := m.updateEvents
	if events == nil {
		return domain.Message{}, false, store.ErrDeliveryOutboxRequired
	}
	// 加锁顺序与 sendPrivateTextLocked 相同：MessageStore → UpdateEventStore → DialogStore。
	m.mu.Lock()
	defer m.mu.Unlock()
	key := contactSignUpKey{owner: n.OwnerUserID, from: n.FromUserID}
	if first, ok := s.written[key]; ok {
		return cloneMessage(first), false, nil
	}
	events.mu.Lock()
	defer events.mu.Unlock()

	from := domain.Peer{Type: domain.PeerTypeUser, ID: n.FromUserID}
	msg := domain.Message{
		ID:          m.nextBoxIDLocked(n.OwnerUserID),
		UID:         m.nextUID,
		OwnerUserID: n.OwnerUserID,
		Peer:        from,
		From:        from,
		Date:        n.Date,
		Silent:      n.Silent,
		Media: &domain.MessageMedia{
			Kind:          domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionContactSignUp},
		},
		Pts: m.nextPtsWithEventsLocked(events, n.OwnerUserID),
	}
	m.nextUID++
	m.m[n.OwnerUserID] = append(m.m[n.OwnerUserID], cloneMessage(msg))
	if m.dialogs != nil {
		m.dialogs.mu.Lock()
		list := upsertMemoryDialog(m.dialogs.m[n.OwnerUserID], domain.Dialog{
			Peer:           from,
			TopMessage:     msg.ID,
			TopMessageDate: msg.Date,
			UnreadCount:    m.privateUnreadCountLocked(n.OwnerUserID, from),
		})
		list.Messages = append(list.Messages, cloneMessage(msg))
		m.dialogs.m[n.OwnerUserID] = list
		m.dialogs.mu.Unlock()
	}
	appendMemorySendEventLocked(events, n.OwnerUserID, newMessageEvent(msg), [8]byte{}, 0)
	s.written[key] = cloneMessage(msg)
	return cloneMessage(msg), true, nil
}

// checkEligible：Owner 与 From 都存在且未注销，Owner 不是 bot。
func (s *ContactSignUpNoticeStore) checkEligible(ctx context.Context, n domain.ContactSignUpNotice) error {
	if n.OwnerUserID <= 0 || n.FromUserID <= 0 || n.OwnerUserID == n.FromUserID {
		return domain.ErrUserNotFound
	}
	owner, found, err := s.users.ByID(ctx, n.OwnerUserID)
	if err != nil {
		return err
	}
	if !found || owner.Deleted || owner.Bot {
		return domain.ErrUserNotFound
	}
	from, found, err := s.users.ByID(ctx, n.FromUserID)
	if err != nil {
		return err
	}
	if !found || from.Deleted {
		return domain.ErrUserNotFound
	}
	return nil
}
