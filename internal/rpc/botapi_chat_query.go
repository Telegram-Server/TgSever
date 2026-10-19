package rpc

import (
	"context"
	"errors"

	"telesrv/internal/domain"
)

// Bot API 查询类方法（getChat / getChatMember / getChatAdministrators /
// getChatMemberCount）。全是读操作，直接以机器人为查看者调用 app 层服务，可见性
// 校验由服务本身完成；结果以 domain 类型返回，由 botapi 层投影。

// botAPIChatChannelView 返回机器人视角的超级群/频道；机器人被踢或看不到时一律当作
// 不存在的会话（官方 "chat not found"）。
func (r *Router) botAPIChatChannelView(ctx context.Context, botID, chatID int64) (domain.ChannelView, error) {
	peer, ok := botAPIPeerFromChatID(chatID)
	if !ok || peer.Type != domain.PeerTypeChannel {
		return domain.ChannelView{}, errors.New("CHAT_ID_INVALID")
	}
	if r == nil || botID == 0 || r.deps.Channels == nil {
		return domain.ChannelView{}, errors.New("BOT_INVALID")
	}
	view, err := r.deps.Channels.GetChannel(ctx, botID, peer.ID)
	if err != nil {
		return domain.ChannelView{}, botAPIChannelSendErr(err)
	}
	if view.Forbidden || view.Channel.Deleted {
		return domain.ChannelView{}, errors.New("CHAT_ID_INVALID")
	}
	return view, nil
}

// BotAPIGetChatChannel 实现超级群/频道的 getChat。
func (r *Router) BotAPIGetChatChannel(ctx context.Context, botID, chatID int64) (domain.ChannelView, error) {
	return r.botAPIChatChannelView(ctx, botID, chatID)
}

// BotAPIGetChatUser 实现私聊的 getChat。简介与生日按对方隐私设置对机器人裁剪，
// 与 users.getFullUser 一致。
func (r *Router) BotAPIGetChatUser(ctx context.Context, botID, userID int64) (domain.User, error) {
	if r == nil || botID == 0 {
		return domain.User{}, errors.New("BOT_INVALID")
	}
	user, err := r.botAPIChatUser(ctx, botID, userID)
	if err != nil {
		if err.Error() == "USER_ID_INVALID" {
			return domain.User{}, errors.New("CHAT_ID_INVALID")
		}
		return domain.User{}, err
	}
	visibility, err := r.userFullPrivacyVisibility(ctx, botID, user.ID)
	if err != nil {
		return domain.User{}, err
	}
	if !visibility[domain.PrivacyKeyAbout] {
		user.About = ""
	}
	if !visibility[domain.PrivacyKeyBirthday] {
		user.Birthday = domain.Birthday{}
	}
	return user, nil
}

// BotAPIGetChatMember 实现 getChatMember：成员记录、其用户资料，以及机器人能否编辑
// 该管理员（can_be_edited）。
func (r *Router) BotAPIGetChatMember(ctx context.Context, botID, chatID, userID int64) (domain.ChannelMember, domain.User, bool, error) {
	view, err := r.botAPIChatChannelView(ctx, botID, chatID)
	if err != nil {
		return domain.ChannelMember{}, domain.User{}, false, err
	}
	member, err := r.deps.Channels.GetParticipant(ctx, botID, view.Channel.ID, userID)
	switch {
	case errors.Is(err, domain.ErrUserNotParticipant):
		// 官方对不在群里的用户返回 status=left，而不是报错。
		member = domain.ChannelMember{ChannelID: view.Channel.ID, UserID: userID, Status: domain.ChannelMemberLeft}
	case err != nil:
		return domain.ChannelMember{}, domain.User{}, false, botAPIChannelSendErr(err)
	}
	user, err := r.botAPIChatUser(ctx, botID, userID)
	if err != nil {
		return domain.ChannelMember{}, domain.User{}, false, err
	}
	return member, user, botAPICanEditAdmin(view.Self, member), nil
}

// BotAPIGetChatAdministrators 实现 getChatAdministrators：群主与全部管理员（含机器人，
// 由 botapi 层按 return_bots 过滤）、他们的用户资料与各自的 can_be_edited。
func (r *Router) BotAPIGetChatAdministrators(ctx context.Context, botID, chatID int64) ([]domain.ChannelMember, []domain.User, map[int64]bool, error) {
	view, err := r.botAPIChatChannelView(ctx, botID, chatID)
	if err != nil {
		return nil, nil, nil, err
	}
	filter := domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsAdmins}
	var members []domain.ChannelMember
	var users []domain.User
	seenUsers := map[int64]bool{}
	for offset := 0; ; {
		page, err := r.deps.Channels.GetParticipants(ctx, botID, view.Channel.ID, filter, offset, domain.MaxChannelParticipantsLimit)
		if err != nil {
			return nil, nil, nil, botAPIChannelSendErr(err)
		}
		members = append(members, page.Participants...)
		for _, u := range page.Users {
			if !seenUsers[u.ID] {
				seenUsers[u.ID] = true
				users = append(users, u)
			}
		}
		offset += len(page.Participants)
		if len(page.Participants) < domain.MaxChannelParticipantsLimit || offset >= page.Count {
			break
		}
	}
	editable := make(map[int64]bool, len(members))
	for _, member := range members {
		if !seenUsers[member.UserID] {
			user, err := r.botAPIChatUser(ctx, botID, member.UserID)
			if err != nil {
				return nil, nil, nil, err
			}
			seenUsers[member.UserID] = true
			users = append(users, user)
		}
		editable[member.UserID] = botAPICanEditAdmin(view.Self, member)
	}
	return members, users, editable, nil
}

// botAPIChatUser 以机器人为查看者读取用户资料。
func (r *Router) botAPIChatUser(ctx context.Context, botID, userID int64) (domain.User, error) {
	if userID <= 0 {
		return domain.User{}, errors.New("USER_ID_INVALID")
	}
	if r.deps.Users == nil {
		return domain.User{}, errors.New("BOT_INVALID")
	}
	user, found, err := r.deps.Users.ByID(ctx, botID, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, errors.New("USER_ID_INVALID")
	}
	return user, nil
}

// botAPICanEditAdmin 判定 can_be_edited：机器人是群主，或有 add_admins 且该管理员由它提拔。
func botAPICanEditAdmin(self, member domain.ChannelMember) bool {
	return domain.CanEditChannelAdmin(self, member)
}

// BotAPIGetChatMemberCount 实现 getChatMemberCount。
func (r *Router) BotAPIGetChatMemberCount(ctx context.Context, botID, chatID int64) (int, error) {
	view, err := r.botAPIChatChannelView(ctx, botID, chatID)
	if err != nil {
		return 0, err
	}
	return view.Channel.ParticipantsCount, nil
}
