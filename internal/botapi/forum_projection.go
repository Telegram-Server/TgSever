package botapi

import (
	"strconv"
	"telesrv/internal/domain"
)

func init() { registerMessageProjection(projectForumMessage) }

func projectForumMessage(msg domain.Message, out map[string]any) {
	if msg.ReplyTo != nil && msg.ReplyTo.ForumTopic && msg.ReplyTo.TopMessageID > 0 {
		out["message_thread_id"], out["is_topic_message"] = msg.ReplyTo.TopMessageID, true
	}
	action := msg.Action
	if action == nil && msg.Media != nil && msg.Media.ServiceAction != nil {
		action = msg.Media.ServiceAction.ForumTopic
	}
	if action == nil {
		return
	}
	switch action.Type {
	case domain.ChannelActionTopicCreate:
		created := map[string]any{"name": action.Title, "icon_color": action.IconColor}
		if action.IconEmojiID != 0 {
			created["icon_custom_emoji_id"] = strconv.FormatInt(action.IconEmojiID, 10)
		}
		out["forum_topic_created"] = created
		if _, ok := out["message_thread_id"]; !ok {
			out["message_thread_id"], out["is_topic_message"] = msg.ID, true
		}
	case domain.ChannelActionTopicEdit:
		if action.Title != "" || action.IconEmojiIDSet {
			edited := map[string]any{}
			if action.Title != "" {
				edited["name"] = action.Title
			}
			if action.IconEmojiIDSet {
				emoji := ""
				if action.IconEmojiID != 0 {
					emoji = strconv.FormatInt(action.IconEmojiID, 10)
				}
				edited["icon_custom_emoji_id"] = emoji
			}
			out["forum_topic_edited"] = edited
		}
		if action.Closed != nil {
			if *action.Closed {
				out["forum_topic_closed"] = map[string]any{}
			} else {
				out["forum_topic_reopened"] = map[string]any{}
			}
		}
		if action.Hidden != nil {
			if *action.Hidden {
				out["general_forum_topic_hidden"] = map[string]any{}
			} else {
				out["general_forum_topic_unhidden"] = map[string]any{}
			}
		}
	}
}
