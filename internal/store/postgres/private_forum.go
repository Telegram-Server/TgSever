package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

func privateForumTopic(row sqlcgen.PrivateForumTopic) domain.PrivateForumTopic {
	return domain.PrivateForumTopic{OwnerUserID: row.OwnerUserID, PeerUserID: row.PeerUserID, ID: int(row.TopicID), PeerTopicID: int(row.PeerTopicID), Name: row.Name, IconColor: int(row.IconColor), IconEmojiID: row.IconEmojiID, Date: int(row.Date), Deleted: row.Deleted}
}
func (s *MessageStore) GetPrivateForumTopic(ctx context.Context, owner, peer int64, id int) (domain.PrivateForumTopic, error) {
	row, err := s.q.GetPrivateForumTopic(ctx, sqlcgen.GetPrivateForumTopicParams{OwnerUserID: owner, PeerUserID: peer, TopicID: int32(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PrivateForumTopic{}, domain.ErrMessageIDInvalid
	}
	if err != nil {
		return domain.PrivateForumTopic{}, err
	}
	return privateForumTopic(row), nil
}
func (s *MessageStore) ResolvePrivateForumTopic(ctx context.Context, owner, peer int64, id int) (domain.PrivateForumTopic, error) {
	row, err := s.q.ResolvePrivateForumTopic(ctx, sqlcgen.ResolvePrivateForumTopicParams{SenderID: owner, RecipientID: peer, TopicID: int32(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PrivateForumTopic{}, domain.ErrMessageIDInvalid
	}
	if err != nil {
		return domain.PrivateForumTopic{}, err
	}
	return privateForumTopic(row), nil
}
func (s *MessageStore) CreatePrivateForumTopic(ctx context.Context, req domain.CreatePrivateForumTopicRequest) (domain.PrivateForumTopicResult, error) {
	if req.RecipientBlocked {
		return domain.PrivateForumTopicResult{}, domain.ErrPrivateForumBlocked
	}
	if req.OwnerUserID <= 0 || req.PeerUserID <= 0 || req.OwnerUserID == req.PeerUserID || req.RandomID == 0 {
		return domain.PrivateForumTopicResult{}, domain.ErrMessageIDInvalid
	}
	if err := domain.ValidatePrivateForumTopicName(req.Name); err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	res, err := s.sendPrivateTextWithHooks(ctx, store.PrivateForumCreateMessage(req), privateSendTxHooks{after: func(ctx context.Context, tx pgx.Tx, res domain.SendPrivateTextResult) error {
		return sqlcgen.New(tx).InsertPrivateForumTopic(ctx, sqlcgen.InsertPrivateForumTopicParams{OwnerUserID: req.OwnerUserID, PeerUserID: req.PeerUserID, TopicID: int32(res.SenderMessage.ID), PeerTopicID: int32(res.RecipientMessage.ID), Name: req.Name, IconColor: int32(req.IconColor), IconEmojiID: req.IconEmojiID, Date: int32(res.SenderMessage.Date)})
	}})
	if err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	topic, err := s.GetPrivateForumTopic(ctx, req.OwnerUserID, req.PeerUserID, res.SenderMessage.ID)
	return domain.PrivateForumTopicResult{Topic: topic, Message: res}, err
}
func (s *MessageStore) EditPrivateForumTopic(ctx context.Context, req domain.EditPrivateForumTopicRequest) (domain.PrivateForumTopicResult, error) {
	if req.RecipientBlocked {
		return domain.PrivateForumTopicResult{}, domain.ErrPrivateForumBlocked
	}
	if req.Name != nil {
		if err := domain.ValidatePrivateForumTopicName(*req.Name); err != nil {
			return domain.PrivateForumTopicResult{}, err
		}
	}
	res, err := s.sendPrivateTextWithHooks(ctx, store.PrivateForumEditMessage(req), privateSendTxHooks{before: func(ctx context.Context, tx pgx.Tx, _ *domain.SendPrivateTextRequest) error {
		q := sqlcgen.New(tx)
		row, err := q.GetPrivateForumTopic(ctx, sqlcgen.GetPrivateForumTopicParams{OwnerUserID: req.OwnerUserID, PeerUserID: req.PeerUserID, TopicID: int32(req.TopicID)})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrMessageIDInvalid
		}
		if err != nil {
			return err
		}
		name, emoji := row.Name, row.IconEmojiID
		if req.Name != nil {
			name = *req.Name
		}
		if req.IconEmojiID != nil {
			emoji = *req.IconEmojiID
		}
		return q.UpdatePrivateForumTopic(ctx, sqlcgen.UpdatePrivateForumTopicParams{OwnerUserID: req.OwnerUserID, PeerUserID: req.PeerUserID, TopicID: int32(req.TopicID), Name: name, IconEmojiID: emoji})
	}})
	if err != nil {
		return domain.PrivateForumTopicResult{}, err
	}
	topic, err := s.GetPrivateForumTopic(ctx, req.OwnerUserID, req.PeerUserID, req.TopicID)
	return domain.PrivateForumTopicResult{Topic: topic, Message: res}, err
}
func (s *MessageStore) DeletePrivateForumTopic(ctx context.Context, owner, peer int64, id, date int) (domain.DeleteMessagesResult, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.DeleteMessagesResult{}, fmt.Errorf("delete private topic: transactions required")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	defer tx.Rollback(ctx)
	if err = lockUsersForUpdate(ctx, tx, owner, peer); err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	if err = lockDispatchOutboxAppendFences(ctx, tx, []int64{owner, peer}); err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	q := sqlcgen.New(tx)
	key := sqlcgen.GetPrivateForumTopicParams{OwnerUserID: owner, PeerUserID: peer, TopicID: int32(id)}
	topic, err := q.GetPrivateForumTopic(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DeleteMessagesResult{}, domain.ErrMessageIDInvalid
	} else if err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	if err = q.MarkPrivateForumTopicDeleted(ctx, sqlcgen.MarkPrivateForumTopicDeletedParams{OwnerUserID: owner, PeerUserID: peer, TopicID: int32(id)}); err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	ids, err := q.PrivateForumTopicMessageIDs(ctx, sqlcgen.PrivateForumTopicMessageIDsParams{OwnerUserID: owner, PeerUserID: peer, TopicID: int32(id)})
	if err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	rows, err := q.DeleteMessageBoxesByIDs(ctx, sqlcgen.DeleteMessageBoxesByIDsParams{OwnerUserID: owner, BoxIds: ids})
	if err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	deleted := deletedRowsFromIDRows(rows)
	if len(deleted) > 0 {
		peers, err := q.DeleteMessageBoxesByPrivateMessages(ctx, privateMessageDeleteParams(deleted))
		if err != nil {
			return domain.DeleteMessagesResult{}, err
		}
		deleted = append(deleted, deletedRowsFromPrivateRows(peers)...)
	}
	peerIDs, err := q.PrivateForumTopicMessageIDs(ctx, sqlcgen.PrivateForumTopicMessageIDsParams{OwnerUserID: peer, PeerUserID: owner, TopicID: topic.PeerTopicID})
	if err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	peerRows, err := q.DeleteMessageBoxesByIDs(ctx, sqlcgen.DeleteMessageBoxesByIDsParams{OwnerUserID: peer, BoxIds: peerIDs})
	if err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	deleted = append(deleted, deletedRowsFromIDRows(peerRows)...)
	audit := domain.DeletionAudit{}.OrUser(domain.DeletionSourceUser, owner, [8]byte{}, 0)
	if err = insertPrivateMessageDeletionsTx(ctx, q, audit, date, domain.PrivateDeletionScope(true), deleted); err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	res, err := s.finishDeleteMessagesTx(ctx, tx, q, owner, [8]byte{}, 0, date, deleted, nil)
	if err != nil {
		return domain.DeleteMessagesResult{}, err
	}
	return res, tx.Commit(ctx)
}
func (s *MessageStore) ListPrivateForumTopicMessages(ctx context.Context, owner, peer int64, id int, filter domain.MessageFilter) (domain.MessageList, error) {
	if _, err := s.q.ResolvePrivateForumTopic(ctx, sqlcgen.ResolvePrivateForumTopicParams{SenderID: owner, RecipientID: peer, TopicID: int32(id)}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = domain.ErrMessageIDInvalid
		}
		return domain.MessageList{}, err
	}
	count, err := s.q.CountPrivateForumTopicMessages(ctx, sqlcgen.CountPrivateForumTopicMessagesParams{OwnerUserID: owner, PeerUserID: peer, TopicID: int32(id), MaxID: int32(filter.MaxID), MinID: int32(filter.MinID)})
	if err != nil {
		return domain.MessageList{}, err
	}
	if filter.CountOnly {
		return domain.MessageList{Count: int(count)}, nil
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	ids, err := s.q.ListPrivateForumTopicMessageIDs(ctx, sqlcgen.ListPrivateForumTopicMessageIDsParams{OwnerUserID: owner, PeerUserID: peer, TopicID: int32(id), MaxID: int32(filter.MaxID), MinID: int32(filter.MinID), OffsetID: int32(filter.OffsetID), OffsetDate: int32(filter.OffsetDate), AddOffset: int32(domain.ClampMessageHistoryAddOffset(filter.AddOffset)), LimitCount: int32(limit)})
	if err != nil {
		return domain.MessageList{}, err
	}
	list, err := s.GetByIDs(ctx, owner, intsFromPrivateForumIDs(ids))
	list.Count = int(count)
	return list, err
}
func intsFromPrivateForumIDs(ids []int32) []int {
	out := make([]int, len(ids))
	for i, id := range ids {
		out[i] = int(id)
	}
	return out
}
func (s *MessageStore) resolvePrivateForumReply(ctx context.Context, q *sqlcgen.Queries, req domain.SendPrivateTextRequest) (*domain.MessageReply, *domain.MessageReply, error) {
	if err := domain.ValidateMessageReplyBounds(req.ReplyTo); err != nil {
		return nil, nil, err
	}
	if req.ReplyTo.StoryID != 0 || req.ReplyTo.External != nil {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	id := req.ReplyTo.TopMessageID
	topic, err := q.ResolvePrivateForumTopic(ctx, sqlcgen.ResolvePrivateForumTopicParams{SenderID: req.SenderUserID, RecipientID: req.RecipientUserID, TopicID: int32(id)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = domain.ErrMessageIDInvalid
		}
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
		recipient.TopMessageID = int(topic.PeerTopicID)
	} else {
		recipient.TopMessageID = int(topic.TopicID)
	}
	if sender.MessageID != 0 {
		uid, err := q.GetPrivateForumReplyTarget(ctx, sqlcgen.GetPrivateForumReplyTargetParams{OwnerUserID: req.SenderUserID, PeerID: req.RecipientUserID, BoxID: int32(sender.MessageID), ReplyToTopID: int32(id)})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				err = domain.ErrReplyMessageIDInvalid
			}
			return nil, nil, err
		}
		source, err := q.GetMessageBoxForReply(ctx, sqlcgen.GetMessageBoxForReplyParams{OwnerUserID: req.SenderUserID, PeerType: string(domain.PeerTypeUser), PeerID: req.RecipientUserID, BoxID: int32(sender.MessageID)})
		if err != nil {
			return nil, nil, err
		}
		message, err := loadPrivateReplyMessage(source, sender.Peer)
		if err != nil {
			return nil, nil, err
		}
		if err := domain.ValidateBotAPIReplySource(req.ReplyTo, message); err != nil {
			return nil, nil, err
		}
		row, err := q.GetMessageBoxByPrivateMessage(ctx, sqlcgen.GetMessageBoxByPrivateMessageParams{OwnerUserID: req.RecipientUserID, PrivateMessageID: uid})
		recipient.MessageID = 0
		if err == nil {
			recipient.MessageID = int(row.BoxID)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
	}
	return sender, recipient, nil
}

func (s *MessageStore) resolvePrivateForumExternalReply(ctx context.Context, db sqlcgen.DBTX, q *sqlcgen.Queries, req domain.SendPrivateTextRequest) (*domain.MessageReply, *domain.MessageReply, error) {
	if err := domain.ValidateMessageReplyBounds(req.ReplyTo); err != nil {
		return nil, nil, err
	}
	if req.ReplyTo.External != nil || req.ReplyTo.StoryID != 0 {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	topic, err := q.ResolvePrivateForumTopic(ctx, sqlcgen.ResolvePrivateForumTopicParams{SenderID: req.SenderUserID, RecipientID: req.RecipientUserID, TopicID: int32(req.ReplyTo.TopMessageID)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = domain.ErrMessageIDInvalid
		}
		return nil, nil, err
	}
	sourceReq := req
	sourceReq.ReplyTo = cloneMessageReply(req.ReplyTo)
	sourceReq.ReplyTo.TopMessageID = 0
	sourceReq.ReplyTo.ForumTopic = false
	sourceReq.ReplyTo.AllowMissing = false
	sender, recipient, err := s.resolvePrivateSendReply(ctx, db, q, sourceReq)
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
		recipient.TopMessageID = int(topic.PeerTopicID)
	} else {
		recipient.TopMessageID = int(topic.TopicID)
	}
	return sender, recipient, nil
}
