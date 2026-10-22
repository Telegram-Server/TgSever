package memory

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func (s *MessageStore) CreatePrivateForumTopic(_ context.Context, req domain.CreatePrivateForumTopicRequest) (domain.PrivateForumTopicResult, error) {
	if req.RecipientBlocked {
		return domain.PrivateForumTopicResult{}, domain.ErrPrivateForumBlocked
	}
	if req.OwnerUserID <= 0 || req.PeerUserID <= 0 || req.OwnerUserID == req.PeerUserID || req.RandomID == 0 {
		return domain.PrivateForumTopicResult{}, domain.ErrMessageIDInvalid
	}
	if err := domain.ValidatePrivateForumTopicName(req.Name); err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	send := store.PrivateForumCreateMessage(req)
	fingerprint, err := store.PrivateSendFingerprint(send)
	if err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.sendPrivateTextLocked(send, fingerprint, nil)
	if err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	key := [3]int64{req.OwnerUserID, req.PeerUserID, int64(res.SenderMessage.ID)}
	if s.privateForumTopics == nil {
		s.privateForumTopics = make(map[[3]int64]domain.PrivateForumTopic)
	}
	topic, ok := s.privateForumTopics[key]
	if !ok {
		topic = domain.PrivateForumTopic{OwnerUserID: req.OwnerUserID, PeerUserID: req.PeerUserID, ID: res.SenderMessage.ID, PeerTopicID: res.RecipientMessage.ID, Name: req.Name, IconColor: req.IconColor, IconEmojiID: req.IconEmojiID, Date: res.SenderMessage.Date}
		s.savePrivateForumTopicLocked(topic)
	}
	if topic.Deleted {
		return domain.PrivateForumTopicResult{}, domain.ErrMessageIDInvalid
	}
	return domain.PrivateForumTopicResult{Topic: topic, Message: res}, nil
}

func (s *MessageStore) privateForumTopicLocked(owner, peer int64, id int) (domain.PrivateForumTopic, error) {
	topic, ok := s.privateForumTopics[[3]int64{owner, peer, int64(id)}]
	if !ok || topic.Deleted || id <= 0 || topic.OwnerUserID != owner {
		return domain.PrivateForumTopic{}, domain.ErrMessageIDInvalid
	}
	return topic, nil
}

func (s *MessageStore) GetPrivateForumTopic(_ context.Context, owner, peer int64, id int) (domain.PrivateForumTopic, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.privateForumTopicLocked(owner, peer, id)
}

func (s *MessageStore) savePrivateForumTopicLocked(topic domain.PrivateForumTopic) {
	s.privateForumTopics[[3]int64{topic.OwnerUserID, topic.PeerUserID, int64(topic.ID)}] = topic
	s.privateForumTopics[[3]int64{topic.PeerUserID, topic.OwnerUserID, int64(topic.PeerTopicID)}] = topic
}
func (s *MessageStore) resolvePrivateForumTopicLocked(owner, peer int64, id int) (domain.PrivateForumTopic, error) {
	topic, ok := s.privateForumTopics[[3]int64{owner, peer, int64(id)}]
	if !ok || topic.Deleted || id <= 0 {
		return domain.PrivateForumTopic{}, domain.ErrMessageIDInvalid
	}
	return topic, nil
}
func (s *MessageStore) ResolvePrivateForumTopic(_ context.Context, owner, peer int64, id int) (domain.PrivateForumTopic, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resolvePrivateForumTopicLocked(owner, peer, id)
}

func (s *MessageStore) EditPrivateForumTopic(_ context.Context, req domain.EditPrivateForumTopicRequest) (domain.PrivateForumTopicResult, error) {
	if req.RecipientBlocked {
		return domain.PrivateForumTopicResult{}, domain.ErrPrivateForumBlocked
	}
	if req.Name != nil {
		if err := domain.ValidatePrivateForumTopicName(*req.Name); err != nil {
			return domain.PrivateForumTopicResult{}, err
		}
	}
	send := store.PrivateForumEditMessage(req)
	fingerprint, err := store.PrivateSendFingerprint(send)
	if err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	topic, err := s.privateForumTopicLocked(req.OwnerUserID, req.PeerUserID, req.TopicID)
	if err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	res, err := s.sendPrivateTextLocked(send, fingerprint, nil)
	if err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	if req.Name != nil {
		topic.Name = *req.Name
	}
	if req.IconEmojiID != nil {
		topic.IconEmojiID = *req.IconEmojiID
	}
	s.savePrivateForumTopicLocked(topic)
	return domain.PrivateForumTopicResult{Topic: topic, Message: res}, nil
}

func (s *MessageStore) DeletePrivateForumTopic(_ context.Context, owner, peer int64, id, date int) (domain.DeleteMessagesResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	topic, err := s.privateForumTopicLocked(owner, peer, id)
	if err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	topic.Deleted = true
	s.savePrivateForumTopicLocked(topic)
	deleted, uids, _ := s.deleteMemoryMessagesLocked(owner, 0, func(msg domain.Message) bool {
		return msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: peer}) && (msg.ID == id || domain.PrivateForumMessageTopic(msg) == id)
	})
	deleted = append(deleted, s.deleteMemoryMessagesByUIDLocked(uids, owner)...)
	peerDeleted, _, _ := s.deleteMemoryMessagesLocked(peer, 0, func(msg domain.Message) bool {
		return msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: owner}) && (msg.ID == topic.PeerTopicID || domain.PrivateForumMessageTopic(msg) == topic.PeerTopicID)
	})
	deleted = append(deleted, peerDeleted...)
	s.recordDeletionsLocked(domain.DeletionAudit{}.OrUser(domain.DeletionSourceUser, owner, [8]byte{}, 0), date, domain.PrivateDeletionScope(true), deleted)
	return s.finishMemoryDeleteLocked(domain.DeleteMessagesResult{OwnerUserID: owner}, deleted, date, nil), nil
}

func (s *MessageStore) ListPrivateForumTopicMessages(_ context.Context, owner, peer int64, id int, filter domain.MessageFilter) (domain.MessageList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.resolvePrivateForumTopicLocked(owner, peer, id); err != nil {
		return domain.MessageList{}, err
	}
	var messages []domain.Message
	for _, msg := range s.m[owner] {
		if msg.Peer == (domain.Peer{Type: domain.PeerTypeUser, ID: peer}) && domain.PrivateForumMessageTopic(msg) == id {
			messages = append(messages, cloneMessage(msg))
		}
	}
	list := filterMessageList(messages, filter)
	list.Hash = messageListHash(list.Messages)
	return list, nil
}

func (s *MessageStore) resolvePrivateForumReplyLocked(req domain.SendPrivateTextRequest) (*domain.MessageReply, *domain.MessageReply, error) {
	if req.ReplyTo.StoryID != 0 || req.ReplyTo.External != nil {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	id := req.ReplyTo.TopMessageID
	topic, err := s.resolvePrivateForumTopicLocked(req.SenderUserID, req.RecipientUserID, id)
	if err != nil {
		return nil, nil, err
	}
	if req.ReplyTo.Peer.ID != 0 && req.ReplyTo.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID}) {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	sender := cloneMessageReply(req.ReplyTo)
	sender.ForumTopic = true
	sender.Peer = domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID}
	recipient := cloneMessageReply(sender)
	recipient.Peer = domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}
	if topic.OwnerUserID == req.SenderUserID {
		recipient.TopMessageID = topic.PeerTopicID
	} else {
		recipient.TopMessageID = topic.ID
	}
	if sender.MessageID != 0 {
		var target domain.Message
		for _, msg := range s.m[req.SenderUserID] {
			if msg.ID == sender.MessageID && msg.Peer == sender.Peer && (domain.PrivateForumMessageTopic(msg) == id || msg.ID == id) {
				target = msg
				break
			}
		}
		if target.ID == 0 || target.Deleted {
			return nil, nil, domain.ErrReplyMessageIDInvalid
		}
		if err := domain.ValidateBotAPIReplySource(req.ReplyTo, target); err != nil {
			return nil, nil, err
		}
		recipient.MessageID = 0
		for _, msg := range s.m[req.RecipientUserID] {
			if msg.UID == target.UID {
				recipient.MessageID = msg.ID
				break
			}
		}
	}
	return sender, recipient, nil
}

func (s *MessageStore) resolvePrivateForumExternalReplyLocked(req domain.SendPrivateTextRequest) (*domain.MessageReply, *domain.MessageReply, error) {
	if err := domain.ValidateMessageReplyBounds(req.ReplyTo); err != nil {
		return nil, nil, err
	}
	if req.ReplyTo.External != nil || req.ReplyTo.StoryID != 0 {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	topic, err := s.resolvePrivateForumTopicLocked(req.SenderUserID, req.RecipientUserID, req.ReplyTo.TopMessageID)
	if err != nil {
		return nil, nil, err
	}
	sourceReq := req
	sourceReq.ReplyTo = cloneMessageReply(req.ReplyTo)
	sourceReq.ReplyTo.TopMessageID = 0
	sourceReq.ReplyTo.ForumTopic = false
	sourceReq.ReplyTo.AllowMissing = false
	sender, recipient, err := s.resolveMemoryReplyLocked(sourceReq)
	if err != nil {
		return nil, nil, err
	}
	if sender == nil || recipient == nil {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	sender.ForumTopic = true
	sender.TopMessageID = req.ReplyTo.TopMessageID
	recipient.ForumTopic = true
	if topic.OwnerUserID == req.SenderUserID {
		recipient.TopMessageID = topic.PeerTopicID
	} else {
		recipient.TopMessageID = topic.ID
	}
	return sender, recipient, nil
}
