CREATE INDEX channel_messages_topic_pinned_idx
ON channel_messages (channel_id, reply_to_top_id, id)
WHERE pinned AND NOT deleted;
