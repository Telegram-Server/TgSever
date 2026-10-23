-- 管理员 bot 收到其他成员的变动（#146）。

-- name: ListChannelAdminBotIDs :many
-- 成员变动的事务里列出群里的管理员 bot（含群主），其他成员的变动发给它们。
SELECT cm.user_id
FROM channel_members cm
JOIN users u ON u.id = cm.user_id
WHERE cm.channel_id = $1
  AND cm.status = 'active'
  AND cm.role IN ('admin', 'creator')
  AND u.is_bot
ORDER BY cm.user_id;