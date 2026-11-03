-- name: ListActiveChannelMemberIDsAfter :many
SELECT user_id
FROM channel_members
WHERE channel_id = $1 AND status = 'active' AND user_id > $2
ORDER BY user_id
LIMIT $3;

-- name: ListActiveChannelBotMemberRoles :many
SELECT m.user_id, m.role
FROM bots b
JOIN channel_members m ON m.user_id = b.bot_user_id
WHERE m.channel_id = $1 AND m.status = 'active'
ORDER BY m.user_id
LIMIT $2;
