package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

type privateForumMessagesService interface {
	CreatePrivateForumTopic(context.Context, int64, domain.CreatePrivateForumTopicRequest) (domain.PrivateForumTopicResult, error)
	EditPrivateForumTopic(context.Context, int64, domain.EditPrivateForumTopicRequest) (domain.PrivateForumTopicResult, error)
	ResolvePrivateForumTopic(context.Context, int64, int64, int) (domain.PrivateForumTopic, error)
	GetPrivateForumTopic(context.Context, int64, int64, int) (domain.PrivateForumTopic, error)
	DeletePrivateForumTopic(context.Context, int64, int64, int, int) (domain.DeleteMessagesResult, error)
	ListPrivateForumTopicMessages(context.Context, int64, int64, int, domain.MessageFilter) (domain.MessageList, error)
}

func (r *Router) botAPIPrivateForum(ctx context.Context, botID, chatID int64) (privateForumMessagesService, error) {
	if r == nil || botID <= 0 || chatID <= 0 || botID == chatID || r.deps.Users == nil {
		return nil, errors.New("CHAT_ID_INVALID")
	}
	bot, found, err := r.deps.Users.ByID(ctx, botID, botID)
	if err != nil {
		return nil, err
	}
	if !found || !bot.Bot {
		return nil, errors.New("BOT_INVALID")
	}
	user, found, err := r.deps.Users.ByID(ctx, botID, chatID)
	if err != nil {
		return nil, err
	}
	if !found || user.Deleted || user.Bot {
		return nil, errors.New("CHAT_ID_INVALID")
	}
	service, ok := r.deps.Messages.(privateForumMessagesService)
	if !ok {
		return nil, internalErr()
	}
	return service, nil
}
func (r *Router) botAPIPrivateTopic(ctx context.Context, botID, chatID int64, threadID int) error {
	service, err := r.botAPIPrivateForum(ctx, botID, chatID)
	if err != nil {
		return err
	}
	if _, err = service.GetPrivateForumTopic(ctx, botID, chatID, threadID); errors.Is(err, domain.ErrMessageIDInvalid) {
		return topicIDInvalidErr()
	}
	return err
}
func (r *Router) botAPICreatePrivateForumTopic(ctx context.Context, botID, chatID int64, name string, iconColor int, iconEmojiID int64) (domain.ChannelForumTopic, error) {
	service, err := r.botAPIPrivateForum(ctx, botID, chatID)
	if err != nil {
		return domain.ChannelForumTopic{}, err
	}
	if err = validateForumTopicTitle(name, false); err != nil {
		return domain.ChannelForumTopic{}, err
	}
	if err = r.privateForumWriteAllowed(ctx, botID, chatID); err != nil {
		return domain.ChannelForumTopic{}, err
	}
	res, err := service.CreatePrivateForumTopic(ctx, botID, domain.CreatePrivateForumTopicRequest{OwnerUserID: botID, PeerUserID: chatID, Name: name, IconColor: iconColor, IconEmojiID: iconEmojiID, RandomID: randomNonZeroInt64(), Date: int(r.clock.Now().Unix())})
	if err != nil {
		return domain.ChannelForumTopic{}, privateForumWriteError(err)
	}
	r.invalidateRPCProjectionForPeer(botID, domain.Peer{Type: domain.PeerTypeUser, ID: chatID})
	r.invalidateRPCProjectionForPeer(chatID, domain.Peer{Type: domain.PeerTypeUser, ID: botID})
	topic := res.Topic
	return domain.ChannelForumTopic{TopicID: topic.ID, CreatorUserID: botID, Title: topic.Name, IconColor: topic.IconColor, IconEmojiID: topic.IconEmojiID, Date: topic.Date}, nil
}
func (r *Router) botAPIEditPrivateForumTopic(ctx context.Context, botID, chatID int64, topicID int, name *string, iconEmojiID *int64) error {
	service, err := r.botAPIPrivateForum(ctx, botID, chatID)
	if err != nil {
		return err
	}
	if name != nil && *name == "" {
		name = nil
	}
	if name != nil {
		if err = validateForumTopicTitle(*name, false); err != nil {
			return err
		}
	}
	if err = r.privateForumWriteAllowed(ctx, botID, chatID); err != nil {
		return err
	}
	_, err = service.EditPrivateForumTopic(ctx, botID, domain.EditPrivateForumTopicRequest{OwnerUserID: botID, PeerUserID: chatID, TopicID: topicID, Name: name, IconEmojiID: iconEmojiID, RandomID: randomNonZeroInt64(), Date: int(r.clock.Now().Unix())})
	if err == nil {
		r.invalidateRPCProjectionForPeer(botID, domain.Peer{Type: domain.PeerTypeUser, ID: chatID})
		r.invalidateRPCProjectionForPeer(chatID, domain.Peer{Type: domain.PeerTypeUser, ID: botID})
	}
	return privateForumWriteError(err)
}
func (r *Router) botAPIDeletePrivateForumTopic(ctx context.Context, botID, chatID int64, topicID int) error {
	service, err := r.botAPIPrivateForum(ctx, botID, chatID)
	if err != nil {
		return err
	}
	_, err = service.DeletePrivateForumTopic(ctx, botID, chatID, topicID, int(r.clock.Now().Unix()))
	if err == nil {
		r.invalidateRPCProjectionForPeer(botID, domain.Peer{Type: domain.PeerTypeUser, ID: chatID})
		r.invalidateRPCProjectionForPeer(chatID, domain.Peer{Type: domain.PeerTypeUser, ID: botID})
	}
	return err
}
func (r *Router) botAPIUnpinAllPrivateForumTopicMessages(ctx context.Context, botID, chatID int64, topicID int) error {
	if err := r.botAPIPrivateTopic(ctx, botID, chatID, topicID); err != nil {
		return err
	}
	for {
		res, err := r.deps.Messages.UnpinAllPrivateMessages(ctx, botID, domain.UnpinAllPrivateMessagesRequest{OwnerUserID: botID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: chatID}, TopicID: topicID, Date: int(r.clock.Now().Unix())})
		if err != nil {
			return err
		}
		r.invalidateRPCProjectionForPeer(botID, domain.Peer{Type: domain.PeerTypeUser, ID: chatID})
		r.invalidateRPCProjectionForPeer(chatID, domain.Peer{Type: domain.PeerTypeUser, ID: botID})
		if res.Offset == 0 {
			return nil
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
}
func (r *Router) privateForumReplies(ctx context.Context, userID, peerID int64, req *tg.MessagesGetRepliesRequest) (tg.MessagesMessagesClass, error) {
	service, ok := r.deps.Messages.(privateForumMessagesService)
	if !ok {
		return nil, topicIDInvalidErr()
	}
	list, err := service.ListPrivateForumTopicMessages(ctx, userID, peerID, req.MsgID, domain.MessageFilter{OffsetID: req.OffsetID, OffsetDate: req.OffsetDate, AddOffset: req.AddOffset, Limit: req.Limit, MaxID: req.MaxID, MinID: req.MinID, NeedTotalCount: true})
	if errors.Is(err, domain.ErrMessageIDInvalid) {
		return nil, topicIDInvalidErr()
	}
	if err != nil {
		return nil, err
	}
	if req.Hash != 0 && req.Hash == list.Hash {
		return &tg.MessagesMessagesNotModified{Count: list.Count}, nil
	}
	return r.tgMessagesMessages(ctx, userID, r.enrichMessageList(ctx, userID, list)), nil
}

func (r *Router) privateForumRPCPeer(ctx context.Context, userID int64, input tg.InputPeerClass) (privateForumMessagesService, domain.Peer, error) {
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return nil, domain.Peer{}, err
	}
	if peer.Type != domain.PeerTypeUser {
		return nil, domain.Peer{}, peerIDInvalidErr()
	}
	service, err := r.botAPIPrivateForum(ctx, userID, peer.ID)
	return service, peer, err
}
func (r *Router) privateForumRPCUpdates(ctx context.Context, userID int64, res domain.PrivateForumTopicResult, randomID int64) tg.UpdatesClass {
	r.invalidateRPCProjectionForPeer(userID, domain.Peer{Type: domain.PeerTypeUser, ID: res.Topic.PeerUserID})
	r.invalidateRPCProjectionForPeer(res.Topic.PeerUserID, domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	messages := []domain.Message{res.Message.SenderMessage}
	return tgPrivateSendResultUpdates(res.Message, randomID, randomID != 0, r.usersForMessageUpdates(ctx, userID, messages), r.chatsForMessageUpdates(ctx, userID, messages))
}
func (r *Router) createPrivateForumTopicRPC(ctx context.Context, userID int64, req *tg.MessagesCreateForumTopicRequest) (tg.UpdatesClass, error) {
	if req.SendAs != nil || req.TitleMissing {
		return nil, peerIDInvalidErr()
	}
	service, peer, err := r.privateForumRPCPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if err = r.privateForumWriteAllowed(ctx, userID, peer.ID); err != nil {
		return nil, err
	}
	res, err := service.CreatePrivateForumTopic(ctx, userID, domain.CreatePrivateForumTopicRequest{OwnerUserID: userID, PeerUserID: peer.ID, Name: req.Title, IconColor: req.IconColor, IconEmojiID: req.IconEmojiID, RandomID: req.RandomID, Date: int(r.clock.Now().Unix())})
	if err != nil {
		return nil, privateForumWriteError(err)
	}
	return r.privateForumRPCUpdates(ctx, userID, res, req.RandomID), nil
}
func (r *Router) editPrivateForumTopicRPC(ctx context.Context, userID int64, req *tg.MessagesEditForumTopicRequest) (tg.UpdatesClass, error) {
	if _, ok := req.GetClosed(); ok {
		return nil, peerIDInvalidErr()
	}
	if _, ok := req.GetHidden(); ok {
		return nil, peerIDInvalidErr()
	}
	service, peer, err := r.privateForumRPCPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if err = r.privateForumWriteAllowed(ctx, userID, peer.ID); err != nil {
		return nil, err
	}
	edit := domain.EditPrivateForumTopicRequest{OwnerUserID: userID, PeerUserID: peer.ID, TopicID: req.TopicID, RandomID: randomNonZeroInt64(), Date: int(r.clock.Now().Unix())}
	if name, ok := req.GetTitle(); ok {
		edit.Name = &name
	}
	if emoji, ok := req.GetIconEmojiID(); ok {
		edit.IconEmojiID = &emoji
	}
	res, err := service.EditPrivateForumTopic(ctx, userID, edit)
	if err != nil {
		return nil, privateForumWriteError(err)
	}
	return r.privateForumRPCUpdates(ctx, userID, res, 0), nil
}
func (r *Router) deletePrivateForumTopicRPC(ctx context.Context, userID int64, req *tg.MessagesDeleteTopicHistoryRequest) (*tg.MessagesAffectedHistory, error) {
	service, peer, err := r.privateForumRPCPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	res, err := service.DeletePrivateForumTopic(ctx, userID, peer.ID, req.TopMsgID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, err
	}
	r.invalidateRPCProjectionForPeer(userID, peer)
	r.invalidateRPCProjectionForPeer(peer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	self := res.Self()
	return &tg.MessagesAffectedHistory{Pts: self.Pts, PtsCount: self.PtsCount}, nil
}

func (r *Router) privateForumCanonicalThread(ctx context.Context, botID, userID int64, threadID int, fromUser bool) (int, error) {
	if threadID == 0 {
		return 0, nil
	}
	service, err := r.botAPIPrivateForum(ctx, botID, userID)
	if err != nil {
		return 0, err
	}
	sender, peer := botID, userID
	if fromUser {
		sender, peer = userID, botID
	}
	topic, err := service.ResolvePrivateForumTopic(ctx, sender, peer, threadID)
	if err != nil {
		return 0, err
	}
	return topic.ID, nil
}
func (r *Router) privateForumPeerThread(ctx context.Context, botID, userID int64, threadID int) (int, error) {
	if threadID == 0 {
		return 0, nil
	}
	service, err := r.botAPIPrivateForum(ctx, botID, userID)
	if err != nil {
		return 0, err
	}
	topic, err := service.GetPrivateForumTopic(ctx, botID, userID, threadID)
	if err != nil {
		return 0, err
	}
	return topic.PeerTopicID, nil
}

func (r *Router) privateForumWriteAllowed(ctx context.Context, botID, userID int64) error {
	blocked, err := r.peerBlocksUser(ctx, botID, userID)
	if err != nil {
		return err
	}
	if blocked {
		return userIsBlockedErr()
	}
	return nil
}

func privateForumWriteError(err error) error {
	if errors.Is(err, domain.ErrPrivateForumBlocked) {
		return userIsBlockedErr()
	}
	return err
}
