-- name: GetChannelSenderBan :one
SELECT * FROM channel_sender_bans WHERE channel_id = $1 AND sender_channel_id = $2;

-- name: GetChannelSenderBanIdentity :one
SELECT b.*, c.title, c.access_hash, c.broadcast, c.megagroup
FROM channel_sender_bans b JOIN channels c ON c.id = b.sender_channel_id
WHERE b.channel_id = $1 AND b.sender_channel_id = $2;

-- name: PutChannelSenderBan :exec
INSERT INTO channel_sender_bans (channel_id, sender_channel_id, banned_by, banned_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (channel_id, sender_channel_id) DO UPDATE SET banned_by = EXCLUDED.banned_by, banned_at = EXCLUDED.banned_at;

-- name: DeleteChannelSenderBan :execrows
DELETE FROM channel_sender_bans WHERE channel_id = $1 AND sender_channel_id = $2;

-- name: ListChannelKickedPeers :many
WITH kicked AS (
 SELECT m.user_id AS peer_id, false AS sender_channel, m.inviter_user_id AS actor_id, m.left_at AS ban_date,
        m.joined_at, m.banned_rights, m.rank,
        ''::text AS sender_title, 0::bigint AS sender_access_hash, false AS sender_broadcast, false AS sender_megagroup
 FROM channel_members m JOIN users u ON u.id = m.user_id
 WHERE m.channel_id = sqlc.arg(channel_id)
   AND (m.status = 'kicked' OR COALESCE((m.banned_rights->>'ViewMessages')::boolean, false))
   AND (COALESCE((m.banned_rights->>'UntilDate')::integer, 0) = 0
        OR COALESCE((m.banned_rights->>'UntilDate')::integer, 0) > sqlc.arg(now_date)::integer)
   AND (sqlc.arg(query)::text = '' OR lower(u.first_name || ' ' || COALESCE(u.last_name, '') || ' ' || COALESCE(u.username, '') || ' ' || u.phone || ' ' || m.user_id::text) LIKE '%' || lower(sqlc.arg(query)) || '%')
 UNION ALL
 SELECT b.sender_channel_id, true, b.banned_by, b.banned_at, 0, '{"ViewMessages":true}'::jsonb, ''::text,
        c.title, c.access_hash, c.broadcast, c.megagroup
 FROM channel_sender_bans b JOIN channels c ON c.id = b.sender_channel_id
 WHERE b.channel_id = sqlc.arg(channel_id)
 AND (sqlc.arg(query)::text = '' OR lower(c.title || ' ' || COALESCE(c.username, '') || ' ' || c.id::text) LIKE '%' || lower(sqlc.arg(query)) || '%')
)
SELECT *, COUNT(*) OVER ()::integer AS total_count FROM kicked
ORDER BY sender_channel DESC, peer_id
LIMIT sqlc.arg(page_limit) OFFSET sqlc.arg(page_offset);

-- name: CountChannelKickedPeers :one
SELECT (
 (SELECT COUNT(*) FROM channel_members m JOIN users u ON u.id = m.user_id
  WHERE m.channel_id = sqlc.arg(channel_id)
  AND (m.status = 'kicked' OR COALESCE((m.banned_rights->>'ViewMessages')::boolean, false))
  AND (COALESCE((m.banned_rights->>'UntilDate')::integer, 0) = 0 OR COALESCE((m.banned_rights->>'UntilDate')::integer, 0) > sqlc.arg(now_date)::integer)
  AND (sqlc.arg(query)::text = '' OR lower(u.first_name || ' ' || COALESCE(u.last_name, '') || ' ' || COALESCE(u.username, '') || ' ' || u.phone || ' ' || m.user_id::text) LIKE '%' || lower(sqlc.arg(query)) || '%'))
 + (SELECT COUNT(*) FROM channel_sender_bans b JOIN channels c ON c.id = b.sender_channel_id
    WHERE b.channel_id = sqlc.arg(channel_id)
    AND (sqlc.arg(query)::text = '' OR lower(c.title || ' ' || COALESCE(c.username, '') || ' ' || c.id::text) LIKE '%' || lower(sqlc.arg(query)) || '%'))
)::integer;
