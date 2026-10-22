-- 频道身份封禁（#137）。

-- name: InsertChannelBannedSenderChat :one
-- 已封禁时不覆盖原记录（保留最初的封禁者与时间），返回 no rows 表示重复封禁。
INSERT INTO channel_banned_sender_chats (channel_id, sender_chat_id, banned_by_user_id, banned_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (channel_id, sender_chat_id) DO NOTHING
RETURNING channel_id, sender_chat_id, banned_by_user_id, banned_at;

-- name: DeleteChannelBannedSenderChat :one
-- 返回 no rows 表示本来就没有封禁。
DELETE FROM channel_banned_sender_chats
WHERE channel_id = $1 AND sender_chat_id = $2
RETURNING channel_id, sender_chat_id, banned_by_user_id, banned_at;

-- name: GetChannelBannedSenderChat :one
SELECT channel_id, sender_chat_id, banned_by_user_id, banned_at
FROM channel_banned_sender_chats
WHERE channel_id = $1 AND sender_chat_id = $2;

-- name: CountChannelBannedSenderChats :one
-- pattern 为空表示不过滤；否则是已转义的 LIKE 模式，匹配频道标题或用户名（小写）。
-- 已删除的频道不计入、不列出（与返回的 SenderChats 一致）；封禁记录保留，仍可解封。
SELECT count(*)
FROM channel_banned_sender_chats b
JOIN channels c ON c.id = b.sender_chat_id
WHERE b.channel_id = sqlc.arg(channel_id)
  AND NOT c.deleted
  AND (sqlc.arg(pattern)::text = ''
       OR lower(c.title) LIKE sqlc.arg(pattern)::text ESCAPE '\'
       OR lower(coalesce(c.username, '')) LIKE sqlc.arg(pattern)::text ESCAPE '\');

-- name: ListChannelBannedSenderChats :many
SELECT b.channel_id, b.sender_chat_id, b.banned_by_user_id, b.banned_at
FROM channel_banned_sender_chats b
JOIN channels c ON c.id = b.sender_chat_id
WHERE b.channel_id = sqlc.arg(channel_id)
  AND NOT c.deleted
  AND (sqlc.arg(pattern)::text = ''
       OR lower(c.title) LIKE sqlc.arg(pattern)::text ESCAPE '\'
       OR lower(coalesce(c.username, '')) LIKE sqlc.arg(pattern)::text ESCAPE '\')
ORDER BY b.sender_chat_id
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);
