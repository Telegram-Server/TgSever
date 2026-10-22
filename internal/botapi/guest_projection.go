package botapi

import "telesrv/internal/domain"

func projectGuestCaller(msg domain.Message, out map[string]any, users map[int64]domain.User, channels map[int64]domain.Channel) {
	caller := msg.GuestChatViaFrom
	if caller.ID == 0 {
		return
	}
	switch caller.Type {
	case domain.PeerTypeUser:
		if user, found := users[caller.ID]; found {
			out["guest_bot_caller_user"] = apiUser(user)
		}
	case domain.PeerTypeChannel:
		if channel, found := channels[caller.ID]; found {
			chat := apiChat(caller, users)
			describeChannelChat(chat, channel)
			out["guest_bot_caller_chat"] = chat
		}
	}
}
