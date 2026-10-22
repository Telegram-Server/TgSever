package store

import (
	"context"
	"telesrv/internal/domain"
)

// PrivateForumStore is implemented by the private message store so topic
// lifecycle, message writes and deletion use the same transaction boundary.
type PrivateForumStore interface {
	CreatePrivateForumTopic(context.Context, domain.CreatePrivateForumTopicRequest) (domain.PrivateForumTopicResult, error)
	EditPrivateForumTopic(context.Context, domain.EditPrivateForumTopicRequest) (domain.PrivateForumTopicResult, error)
	GetPrivateForumTopic(context.Context, int64, int64, int) (domain.PrivateForumTopic, error)
	ResolvePrivateForumTopic(context.Context, int64, int64, int) (domain.PrivateForumTopic, error)
	DeletePrivateForumTopic(context.Context, int64, int64, int, int) (domain.DeleteMessagesResult, error)
	ListPrivateForumTopicMessages(context.Context, int64, int64, int, domain.MessageFilter) (domain.MessageList, error)
}

func PrivateForumCreateMessage(req domain.CreatePrivateForumTopicRequest) domain.SendPrivateTextRequest {
	return domain.SendPrivateTextRequest{SenderUserID: req.OwnerUserID, RecipientUserID: req.PeerUserID, RandomID: req.RandomID, Date: req.Date,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionTopicCreate, ForumTopic: &domain.ChannelMessageAction{Type: domain.ChannelActionTopicCreate, Title: req.Name, IconColor: req.IconColor, IconEmojiID: req.IconEmojiID}}}}
}

func PrivateForumEditMessage(req domain.EditPrivateForumTopicRequest) domain.SendPrivateTextRequest {
	action := &domain.ChannelMessageAction{Type: domain.ChannelActionTopicEdit}
	if req.Name != nil {
		action.Title = *req.Name
	}
	if req.IconEmojiID != nil {
		action.IconEmojiID = *req.IconEmojiID
		action.IconEmojiIDSet = true
	}
	return domain.SendPrivateTextRequest{SenderUserID: req.OwnerUserID, RecipientUserID: req.PeerUserID, RandomID: req.RandomID, Date: req.Date,
		ReplyTo: &domain.MessageReply{TopMessageID: req.TopicID, ForumTopic: true},
		Media:   &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionTopicEdit, ForumTopic: action}}}
}
