-- name: UnpinForumChannelMessages :many
UPDATE channel_messages AS cm SET pinned = false
WHERE cm.channel_id = sqlc.arg(channel_id) AND cm.pinned AND NOT cm.deleted
  AND ((sqlc.arg(topic_id)::int > 1 AND (cm.id = sqlc.arg(topic_id) OR cm.reply_to_top_id = sqlc.arg(topic_id)))
    OR (sqlc.arg(topic_id)::int = 1 AND cm.reply_to_top_id IN (0, 1)
      AND NOT EXISTS (SELECT 1 FROM channel_forum_topics ft
        WHERE ft.channel_id = cm.channel_id AND ft.topic_id = cm.id AND ft.topic_id <> 1)))
RETURNING cm.id;

-- name: HighestPinnedForumChannelMessage :one
SELECT COALESCE(MAX(id), 0)::int AS id FROM channel_messages
WHERE channel_id = $1 AND pinned AND NOT deleted;

-- name: EnsureGeneralForumTopic :exec
INSERT INTO channel_forum_topics (channel_id, topic_id, creator_user_id, title, icon_color, date, top_message_id)
SELECT id, 1, creator_user_id, 'General', 7322096, date, 0 FROM channels WHERE id = $1
ON CONFLICT (channel_id, topic_id) DO NOTHING;

-- name: UpdateForumTopicMetadata :exec
UPDATE channel_forum_topics SET title = sqlc.arg(title), icon_emoji_id = sqlc.arg(icon_emoji_id),
  closed = sqlc.arg(closed), hidden = sqlc.arg(hidden),
  top_message_id = GREATEST(top_message_id, sqlc.arg(top_message_id)::int), updated_at = now()
WHERE channel_id = sqlc.arg(channel_id) AND topic_id = sqlc.arg(topic_id) AND NOT deleted;

-- name: LockForumTopicChannel :exec
SELECT id FROM channels WHERE id = $1 FOR UPDATE;
