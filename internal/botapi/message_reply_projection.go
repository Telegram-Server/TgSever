package botapi

import (
	"encoding/base64"
	"telesrv/internal/domain"
)

func init() {
	registerMessageProjection(func(message domain.Message, out map[string]any) {
		reply := message.ReplyTo
		if reply == nil {
			return
		}
		if reply.TopMessageID > 0 {
			out["message_thread_id"] = reply.TopMessageID
		}
		if reply.ForumTopic {
			out["is_topic_message"] = true
		}
		if reply.ChecklistTaskID > 0 {
			out["reply_to_checklist_task_id"] = reply.ChecklistTaskID
		}
		if len(reply.PollOption) > 0 {
			out["reply_to_poll_option_id"] = base64.RawURLEncoding.EncodeToString(reply.PollOption)
		}
		if reply.QuoteText != "" {
			quote := map[string]any{"text": reply.QuoteText, "position": reply.QuoteOffset, "is_manual": true}
			if entities := apiMessageEntities(reply.QuoteEntities, nil); len(entities) > 0 {
				quote["entities"] = entities
			}
			out["quote"] = quote
		}
	})
}
