-- 消息反应发给 bot（#150）：反应写入的事务里读出旧反应、计数与能收到的 bot。

-- name: ListUserChannelMessageReactions :many
SELECT reaction_type, reaction_value
FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND reacted_user_id = $3
ORDER BY chosen_order;

-- name: CountChannelMessageReactions :many
SELECT reaction_type, reaction_value, count(*)::integer AS reaction_count
FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2
GROUP BY reaction_type, reaction_value
ORDER BY count(*) DESC, max(reaction_date) DESC, reaction_type, reaction_value;

-- name: ListChannelAdminBotMembers :many
-- 频道里身为创建者/管理员且仍在群里的 bot 账号。
SELECT m.user_id, m.role, m.status, m.available_min_id
FROM channel_members m
JOIN users u ON u.id = m.user_id
WHERE m.channel_id = $1
  AND m.status = 'active'
  AND m.role IN ('creator', 'admin')
  AND u.is_bot
ORDER BY m.user_id;

-- name: ListUserPrivateMessageReactions :many
SELECT reaction_type, reaction_value
FROM private_message_reactions
WHERE message_sender_id = $1 AND private_message_id = $2 AND user_id = $3
ORDER BY chosen_order;
