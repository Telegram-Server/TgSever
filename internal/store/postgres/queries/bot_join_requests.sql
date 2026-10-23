-- 入群申请发给管理员 bot（#149）。

-- name: ListChannelAdminBotIDs :many
-- 写入入群申请的事务里列出群里在群的 bot 管理员（含群主）；是否有邀请权限由领域规则判定。
SELECT m.user_id
FROM channel_members m
JOIN users u ON u.id = m.user_id
WHERE m.channel_id = $1
  AND m.status = 'active'
  AND m.role IN ('creator', 'admin')
  AND u.is_bot
ORDER BY m.user_id;

-- name: GetJoinRequesterAbout :one
SELECT about
FROM users
WHERE id = $1;
