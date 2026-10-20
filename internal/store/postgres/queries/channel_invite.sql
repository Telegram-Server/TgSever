-- name: UpdateExportedChannelInvite :exec
UPDATE channel_invites
SET title = sqlc.arg(title),
    expire_date = NULLIF(sqlc.arg(expire_date)::integer, 0),
    usage_limit = NULLIF(sqlc.arg(usage_limit)::integer, 0),
    request_needed = sqlc.arg(request_needed),
    updated_at = now()
WHERE channel_id = sqlc.arg(channel_id) AND invite_id = sqlc.arg(invite_id);

-- name: DeleteExportedChannelInvite :execrows
WITH deleted AS (
    DELETE FROM channel_invites AS invites
    WHERE invites.channel_id = sqlc.arg(channel_id) AND invites.hash = sqlc.arg(hash)
    RETURNING invites.channel_id, invites.invite_id, invites.hash
), cleared AS (
    DELETE FROM channel_invite_importers AS importers
    USING deleted
    WHERE importers.channel_id = deleted.channel_id AND importers.invite_id = deleted.invite_id AND importers.requested
)
DELETE FROM channel_invite_hashes AS hashes
USING deleted
WHERE hashes.hash = deleted.hash;

-- name: ListChannelInviteAdminCounts :many
SELECT admin_user_id,
       COUNT(*) FILTER (WHERE NOT revoked)::integer AS active_count,
       COUNT(*) FILTER (WHERE revoked)::integer AS revoked_count
FROM channel_invites
WHERE channel_id = sqlc.arg(channel_id)
  AND (sqlc.arg(creator)::boolean OR admin_user_id = sqlc.arg(admin_user_id))
GROUP BY admin_user_id
ORDER BY admin_user_id;

-- name: ListChannelInviteAdminMemberIDs :many
SELECT user_id
FROM channel_members
WHERE channel_id = sqlc.arg(channel_id) AND status = 'active'
  AND (role = 'creator' OR (role = 'admin' AND (admin_rights->>'InviteUsers')::boolean IS TRUE))
ORDER BY user_id
LIMIT sqlc.arg('limit');
