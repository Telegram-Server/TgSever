-- name: IncrementChannelPostForwards :exec
-- 转发写入事务里给源帖子的转发数原子加一；不是 broadcast post 或已删除时不改任何行。
UPDATE channel_messages
SET forwards_count = forwards_count + 1,
    updated_at = now()
WHERE channel_id = sqlc.arg(channel_id)::bigint
  AND id = sqlc.arg(id)::int
  AND post
  AND NOT deleted;
