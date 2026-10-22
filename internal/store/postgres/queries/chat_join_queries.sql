-- name: GetChannelGuardBot :one
SELECT g.bot_user_id FROM channel_guard_bots g
JOIN channels c ON c.id=g.channel_id
JOIN channel_members m ON m.channel_id=g.channel_id AND m.user_id=g.bot_user_id
WHERE g.channel_id=$1 AND c.join_request AND NOT c.deleted AND m.status='active'
AND (m.role='creator' OR (m.role='admin' AND COALESCE((m.admin_rights->>'InviteUsers')::boolean,false)));

-- name: GetChannelGuardBotForUpdate :one
SELECT bot_user_id FROM channel_guard_bots WHERE channel_id = $1 FOR UPDATE;

-- name: PutChannelGuardBot :exec
INSERT INTO channel_guard_bots(channel_id,bot_user_id) VALUES ($1,$2)
ON CONFLICT(channel_id) DO UPDATE SET bot_user_id=EXCLUDED.bot_user_id;

-- name: DeleteChannelGuardBot :exec
DELETE FROM channel_guard_bots WHERE channel_id=$1;

-- name: SetChannelGuardJoinRequest :exec
UPDATE channels SET join_request=$2 WHERE id=$1;

-- name: InsertChatJoinQuery :one
INSERT INTO chat_join_queries(id,channel_id,bot_user_id,user_id,request_date,expires_at,state,web_app_url)
VALUES ($1,$2,$3,$4,$5,$6,'pending','')
ON CONFLICT(channel_id,user_id,request_date) DO UPDATE SET id=chat_join_queries.id
RETURNING *;

-- name: GetChatJoinQuery :one
SELECT q.* FROM chat_join_queries q
WHERE q.id=$1 AND EXISTS (
 SELECT 1 FROM channel_invite_importers i
 WHERE i.channel_id=q.channel_id AND i.user_id=q.user_id
 AND i.requested AND i.date=q.request_date
);

-- name: GetChatJoinQueryForUpdate :one
SELECT * FROM chat_join_queries WHERE id=$1 FOR UPDATE;

-- name: UpdateChatJoinQuery :exec
UPDATE chat_join_queries SET state=$2,web_app_url=$3,expires_at=$4 WHERE id=$1;

-- name: ListExpiredChatJoinQueries :many
SELECT * FROM chat_join_queries
WHERE state IN ('pending','webview') AND expires_at <= sqlc.arg(now_date)::integer
ORDER BY expires_at,id LIMIT sqlc.arg(page_limit)::integer;

-- name: DeleteOldChatJoinQueries :exec
DELETE FROM chat_join_queries WHERE id IN (
 SELECT id FROM chat_join_queries WHERE expires_at < sqlc.arg(cutoff)::integer
 ORDER BY expires_at,id LIMIT sqlc.arg(page_limit)::integer
);

-- name: IsChannelSenderOwnerBanned :one
SELECT EXISTS (
 SELECT 1 FROM channel_sender_bans b
 JOIN channels banned ON banned.id=b.sender_channel_id
 JOIN channels sender ON sender.id=sqlc.arg(sender_channel_id)::bigint
 WHERE b.channel_id=sqlc.arg(channel_id)::bigint
 AND (b.sender_channel_id=sender.id OR banned.creator_user_id=sender.creator_user_id)
);
