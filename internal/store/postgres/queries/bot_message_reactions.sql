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

-- name: ListUserPrivateMessageReactions :many
SELECT reaction_type, reaction_value
FROM private_message_reactions
WHERE message_sender_id = $1 AND private_message_id = $2 AND user_id = $3
ORDER BY chosen_order;
