-- name: ReplaceChannelMessageMedia :exec
-- 编辑替换频道消息的媒体快照（live location、链接预览重算）；'{}' 表示无媒体。
UPDATE channel_messages
SET media = sqlc.arg(media_json)::jsonb,
    updated_at = now()
WHERE channel_id = sqlc.arg(channel_id)::bigint
  AND id = sqlc.arg(id)::int;
