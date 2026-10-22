package botapi

import (
	"strings"

	"telesrv/internal/domain"
)

func apiInviteLink(invite domain.ChannelInvite, creator domain.User, viewerID int64) map[string]any {
	ownsLink := viewerID > 0 && viewerID == invite.AdminUserID
	link := invite.Link
	if link == "" {
		link = "https://t.me/+..."
		if ownsLink {
			link = "https://t.me/+" + invite.Hash
		}
	} else if !ownsLink {
		// Bot API ChatInviteLink hides the secret part of another admin's link.
		// Strip the entire final component, including any query/fragment.
		separator := strings.LastIndexByte(link, '/')
		if separator < 0 {
			link = "..."
		} else {
			prefix := separator + 1
			if strings.HasPrefix(link[prefix:], "+") {
				prefix++
			}
			link = link[:prefix] + "..."
		}
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
	if invite.SubscriptionPeriod > 0 {
		out["subscription_period"] = invite.SubscriptionPeriod
		out["subscription_price"] = invite.SubscriptionPrice
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
		out["invite_link"] = apiInviteLink(*u.Invite, userOf(u.Invite.AdminUserID), event.UserID)
	}
	return out, true
}
