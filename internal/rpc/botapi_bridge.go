package rpc

import (
	"context"
	"errors"
	"strings"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Bot API 桥接：HTTP 网关的写操作以机器人身份直接调用 MTProto 处理器
// （r.onXxx(WithUserID(ctx, botID), req)），从而继承处理器里的权限校验、服务消息、
// 缓存失效、在线成员维护与更新扇出——这些都不在 app 层，绕过处理器直接调 app 层会
// 让其他客户端看不到变化。本文件提供处理器需要的 TL 输入，以及从返回的 Updates
// 取回持久化消息的工具。

// BotAPIResolveChatUsername 把 @channelusername 解析成 Bot API chat_id。官方网关只
// 接受频道/超级群的用户名，用户只能用数字 id 指定。
func (r *Router) BotAPIResolveChatUsername(ctx context.Context, botID int64, username string) (int64, error) {
	if r == nil || botID == 0 || r.deps.Channels == nil {
		return 0, errors.New("BOT_INVALID")
	}
	channel, found, err := r.deps.Channels.ResolvePublicUsername(ctx, botID, strings.TrimSpace(username))
	if err != nil {
		return 0, usernameErr(err)
	}
	if !found || channel.ID <= 0 {
		return 0, errors.New("CHAT_ID_INVALID")
	}
	return -botAPIChannelChatIDBase - channel.ID, nil
}

// botAPIInputChannel 把超级群/频道的 chat_id 变成带 access_hash 的 InputChannel；
// 同时返回机器人视角的 ChannelView，调用方据此做角色判断时不必再查一次。
func (r *Router) botAPIInputChannel(ctx context.Context, botID, chatID int64) (*tg.InputChannel, domain.ChannelView, error) {
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok || peer.Type != domain.PeerTypeChannel {
		return nil, domain.ChannelView{}, errors.New("CHAT_ID_INVALID")
	}
	if r.deps.Channels == nil {
		return nil, domain.ChannelView{}, errors.New("BOT_INVALID")
	}
	view, err := r.deps.Channels.ResolveChannel(ctx, botID, peer.ID)
	if err != nil {
		return nil, domain.ChannelView{}, botAPIChannelSendErr(err)
	}
	return &tg.InputChannel{ChannelID: view.Channel.ID, AccessHash: view.Channel.AccessHash}, view, nil
}

// botAPIInputUser 返回机器人可见用户的 InputUser。
func (r *Router) botAPIInputUser(ctx context.Context, botID, userID int64) (*tg.InputUser, error) {
	if userID <= 0 {
		return nil, errors.New("USER_ID_INVALID")
	}
	if userID == botID {
		return &tg.InputUser{UserID: botID}, nil
	}
	if r.deps.Users == nil {
		return nil, errors.New("BOT_INVALID")
	}
	user, found, err := r.deps.Users.ByID(ctx, botID, userID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("USER_ID_INVALID")
	}
	return &tg.InputUser{UserID: user.ID, AccessHash: user.AccessHash}, nil
}

// botAPIInputPeer 把任意 chat_id 变成 InputPeer 与对应的 domain.Peer。
func (r *Router) botAPIInputPeer(ctx context.Context, botID, chatID int64) (tg.InputPeerClass, domain.Peer, error) {
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok {
		return nil, domain.Peer{}, errors.New("CHAT_ID_INVALID")
	}
	if peer.Type == domain.PeerTypeChannel {
		input, _, err := r.botAPIInputChannel(ctx, botID, chatID)
		if err != nil {
			return nil, domain.Peer{}, err
		}
		return &tg.InputPeerChannel{ChannelID: input.ChannelID, AccessHash: input.AccessHash}, peer, nil
	}
	user, err := r.botAPIInputUser(ctx, botID, peer.ID)
	if err != nil {
		return nil, domain.Peer{}, errors.New("CHAT_ID_INVALID")
	}
	return &tg.InputPeerUser{UserID: user.UserID, AccessHash: user.AccessHash}, peer, nil
}

// botAPIMessagesFromUpdates 按出现顺序收集 Updates 里新建/编辑的消息 id，再以机器人
// 身份重读成 domain.Message（与 BotAPISendMessage 返回值同形）。peer 是请求的目标
// 会话：UpdateShortSentMessage 不带 peer，只能靠它补全。
func (r *Router) botAPIMessagesFromUpdates(ctx context.Context, botID int64, peer domain.Peer, updates tg.UpdatesClass) ([]domain.Message, error) {
	ids := botAPIUpdateMessageIDs(peer, updates)
	if len(ids) == 0 {
		return nil, nil
	}
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return nil, errors.New("BOT_INVALID")
		}
		history, err := r.deps.Channels.GetMessages(ctx, botID, peer.ID, ids)
		if err != nil {
			return nil, botAPIChannelSendErr(err)
		}
		byID := make(map[int]domain.ChannelMessage, len(history.Messages))
		for _, msg := range history.Messages {
			byID[msg.ID] = msg
		}
		out := make([]domain.Message, 0, len(ids))
		for _, id := range ids {
			if msg, ok := byID[id]; ok {
				out = append(out, botAPIMessageFromChannel(botID, msg))
			}
		}
		return out, nil
	}
	if r.deps.Messages == nil {
		return nil, errors.New("BOT_INVALID")
	}
	list, err := r.deps.Messages.GetMessages(ctx, botID, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]domain.Message, len(list.Messages))
	for _, msg := range list.Messages {
		byID[msg.ID] = msg
	}
	out := make([]domain.Message, 0, len(ids))
	for _, id := range ids {
		if msg, ok := byID[id]; ok {
			out = append(out, msg)
		}
	}
	return out, nil
}

// botAPIUpdateMessageIDs 只取属于 peer 的消息 id，去重并保持顺序。
func botAPIUpdateMessageIDs(peer domain.Peer, updates tg.UpdatesClass) []int {
	var ids []int
	seen := map[int]bool{}
	add := func(id int) {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	addMessage := func(msg tg.MessageClass) {
		switch m := msg.(type) {
		case *tg.Message:
			if botAPIPeerMatches(peer, m.PeerID) {
				add(m.ID)
			}
		case *tg.MessageService:
			if botAPIPeerMatches(peer, m.PeerID) {
				add(m.ID)
			}
		}
	}
	var list []tg.UpdateClass
	switch u := updates.(type) {
	case *tg.Updates:
		list = u.Updates
	case *tg.UpdatesCombined:
		list = u.Updates
	case *tg.UpdateShort:
		list = []tg.UpdateClass{u.Update}
	case *tg.UpdateShortSentMessage:
		add(u.ID)
	}
	for _, update := range list {
		switch v := update.(type) {
		case *tg.UpdateNewMessage:
			addMessage(v.Message)
		case *tg.UpdateNewChannelMessage:
			addMessage(v.Message)
		case *tg.UpdateEditMessage:
			addMessage(v.Message)
		case *tg.UpdateEditChannelMessage:
			addMessage(v.Message)
		}
	}
	return ids
}

func botAPIPeerMatches(peer domain.Peer, wire tg.PeerClass) bool {
	switch p := wire.(type) {
	case *tg.PeerUser:
		return peer.Type == domain.PeerTypeUser && peer.ID == p.UserID
	case *tg.PeerChannel:
		return peer.Type == domain.PeerTypeChannel && peer.ID == p.ChannelID
	default:
		return false
	}
}
