package botapi

import "telesrv/internal/domain"

func apiInviteLink(invite domain.ChannelInvite, creator domain.User) map[string]any {
	link := invite.Link
	if link == "" {
		link = "https://t.me/+" + invite.Hash
	}
	out := map[string]any{"invite_link": link, "creator": apiUser(creator), "creates_join_request": invite.RequestNeeded, "is_primary": invite.Permanent, "is_revoked": invite.Revoked}
	if invite.Title != "" {
		out["name"] = invite.Title
	}
	if invite.ExpireDate > 0 {
		out["expire_date"] = invite.ExpireDate
	}
	if invite.UsageLimit > 0 {
		out["member_limit"] = invite.UsageLimit
	}
	if invite.RequestNeeded {
		out["pending_join_request_count"] = invite.RequestedCount
	}
	return out
}

func apiChatJoinRequest(event domain.UpdateEvent) (map[string]any, bool) {
	u := event.BotJoinRequest
	if u == nil || u.UserID <= 0 || u.UserChatID <= 0 || u.Chat.Type != domain.PeerTypeChannel || u.Chat.ID <= 0 || u.Date <= 0 {
		return nil, false
	}
	users := make(map[int64]domain.User, len(event.Users))
	for _, user := range event.Users {
		users[user.ID] = user
	}
	userOf := func(id int64) domain.User {
		if user, ok := users[id]; ok {
			return user
		}
		return domain.User{ID: id}
	}
	chat := apiChat(u.Chat, users)
	for _, channel := range event.Channels {
		if channel.ID == u.Chat.ID {
			describeChannelChat(chat, channel)
			break
		}
	}
	out := map[string]any{"chat": chat, "from": apiUser(userOf(u.UserID)), "user_chat_id": u.UserChatID, "date": u.Date}
	if u.About != "" {
		out["bio"] = u.About
	}
	if u.Invite != nil {
		out["invite_link"] = apiInviteLink(*u.Invite, userOf(u.Invite.AdminUserID))
	}
	return out, true
}
