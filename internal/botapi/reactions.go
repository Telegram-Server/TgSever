package botapi

import (
	"strconv"
	"telesrv/internal/domain"
)

func apiReaction(reaction domain.MessageReaction) map[string]any {
	if reaction.Type == domain.MessageReactionCustomEmoji {
		return map[string]any{"type": "custom_emoji", "custom_emoji_id": strconv.FormatInt(reaction.DocumentID, 10)}
	}
	return map[string]any{"type": "emoji", "emoji": reaction.Emoticon}
}

func apiReactionUpdated(event domain.UpdateEvent) (map[string]any, string, bool) {
	u := event.BotReaction
	if u == nil || u.MessageID <= 0 || u.Chat.ID <= 0 || u.Date <= 0 || !domain.ReactionActorValid(u.Actor()) {
		return nil, "", false
	}
	users := make(map[int64]domain.User, len(event.Users))
	for _, user := range event.Users {
		users[user.ID] = user
	}
	chat := apiChat(u.Chat, users)
	for _, channel := range event.Channels {
		if u.Chat.Type == domain.PeerTypeChannel && channel.ID == u.Chat.ID {
			describeChannelChat(chat, channel)
			break
		}
	}
	out := map[string]any{"chat": chat, "message_id": u.MessageID, "date": u.Date}
	if event.Type == domain.UpdateEventBotMessageReactionCount {
		counts := make([]map[string]any, 0, len(u.Counts))
		for _, count := range u.Counts {
			counts = append(counts, map[string]any{"type": apiReaction(count.Reaction), "total_count": count.Count})
		}
		out["reactions"] = counts
		return out, string(domain.BotAPIUpdateMessageReactionCount), true
	}
	if u.ActorChatID > 0 {
		actorChat := apiChat(domain.Peer{Type: domain.PeerTypeChannel, ID: u.ActorChatID}, users)
		for _, channel := range event.Channels {
			if channel.ID == u.ActorChatID {
				describeChannelChat(actorChat, channel)
				break
			}
		}
		out["actor_chat"] = actorChat
	} else {
		actor := users[u.ActorUserID]
		if actor.ID == 0 {
			actor.ID = u.ActorUserID
		}
		out["user"] = apiUser(actor)
	}
	old, next := make([]map[string]any, 0, len(u.OldReactions)), make([]map[string]any, 0, len(u.NewReactions))
	for _, reaction := range u.OldReactions {
		old = append(old, apiReaction(reaction))
	}
	for _, reaction := range u.NewReactions {
		next = append(next, apiReaction(reaction))
	}
	out["old_reaction"], out["new_reaction"] = old, next
	return out, string(domain.BotAPIUpdateMessageReaction), true
}
