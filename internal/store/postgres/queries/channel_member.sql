-- name: ListActiveChannelMemberIDsAfter :many
SELECT user_id
FROM channel_members
WHERE channel_id = $1 AND status = 'active' AND user_id > $2
ORDER BY user_id
LIMIT $3;
