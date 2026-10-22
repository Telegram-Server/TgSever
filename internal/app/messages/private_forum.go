package messages

import (
	"context"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func (s *Service) privateForumStore() (store.PrivateForumStore, error) {
	if s == nil {
		return nil, domain.ErrMessageIDInvalid
	}
	topics, ok := s.messages.(store.PrivateForumStore)
	if !ok {
		return nil, domain.ErrMessageIDInvalid
	}
	return topics, nil
}
func (s *Service) CreatePrivateForumTopic(ctx context.Context, userID int64, req domain.CreatePrivateForumTopicRequest) (domain.PrivateForumTopicResult, error) {
	if userID <= 0 || req.OwnerUserID != userID {
		return domain.PrivateForumTopicResult{}, domain.ErrAuthenticatedScopeInvalid
	}
	if err := s.ensureCanSend(ctx, userID); err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	if err := s.ensureCanSendRecipientAccess(ctx, req.PeerUserID, userID); err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	if s.contacts != nil {
		blocked, err := s.contacts.IsBlocked(ctx, req.PeerUserID, userID)
		if err != nil {
			return domain.PrivateForumTopicResult{}, err
		}
		req.RecipientBlocked = req.RecipientBlocked || blocked
	}
	topics, err := s.privateForumStore()
	if err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	return topics.CreatePrivateForumTopic(ctx, req)
}
func (s *Service) EditPrivateForumTopic(ctx context.Context, userID int64, req domain.EditPrivateForumTopicRequest) (domain.PrivateForumTopicResult, error) {
	if userID <= 0 || req.OwnerUserID != userID {
		return domain.PrivateForumTopicResult{}, domain.ErrAuthenticatedScopeInvalid
	}
	if err := s.ensureCanSend(ctx, userID); err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	if err := s.ensureCanSendRecipientAccess(ctx, req.PeerUserID, userID); err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	if s.contacts != nil {
		blocked, err := s.contacts.IsBlocked(ctx, req.PeerUserID, userID)
		if err != nil {
			return domain.PrivateForumTopicResult{}, err
		}
		req.RecipientBlocked = req.RecipientBlocked || blocked
	}
	topics, err := s.privateForumStore()
	if err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	return topics.EditPrivateForumTopic(ctx, req)
}
func (s *Service) GetPrivateForumTopic(ctx context.Context, owner, peer int64, id int) (domain.PrivateForumTopic, error) {
	topics, err := s.privateForumStore()
	if err != nil {
		return domain.PrivateForumTopic{}, err
	}
	return topics.GetPrivateForumTopic(ctx, owner, peer, id)
}
func (s *Service) DeletePrivateForumTopic(ctx context.Context, owner, peer int64, id, date int) (domain.DeleteMessagesResult, error) {
	if owner <= 0 || peer <= 0 || owner == peer {
		return domain.DeleteMessagesResult{}, domain.ErrMessageIDInvalid
	}
	topics, err := s.privateForumStore()
	if err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	return topics.DeletePrivateForumTopic(ctx, owner, peer, id, date)
}
func (s *Service) ListPrivateForumTopicMessages(ctx context.Context, owner, peer int64, id int, filter domain.MessageFilter) (domain.MessageList, error) {
	topics, err := s.privateForumStore()
	if err != nil {
		return domain.MessageList{}, err
	}
	list, err := topics.ListPrivateForumTopicMessages(ctx, owner, peer, id, filter)
	if err != nil {
		return domain.MessageList{}, err
	}
	return s.projectMessageUsers(ctx, owner, list)
}

func (s *Service) ResolvePrivateForumTopic(ctx context.Context, owner, peer int64, id int) (domain.PrivateForumTopic, error) {
	topics, err := s.privateForumStore()
	if err != nil {
		return domain.PrivateForumTopic{}, err
	}
	return topics.ResolvePrivateForumTopic(ctx, owner, peer, id)
}
