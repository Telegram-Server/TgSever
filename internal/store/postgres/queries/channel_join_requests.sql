-- name: ListPendingJoinRequestTargetsForUpdate :many
SELECT user_id
FROM channel_invite_importers
WHERE channel_id = sqlc.arg(channel_id) AND requested
  AND (sqlc.arg(invite_id)::bigint = 0 OR invite_id = sqlc.arg(invite_id))
ORDER BY date ASC, user_id ASC
LIMIT sqlc.arg('limit')
FOR UPDATE;

-- name: ClearPendingDirectInviteRequests :one
WITH removed AS (
 DELETE FROM channel_invite_importers AS importers
 WHERE importers.channel_id = sqlc.arg(channel_id) AND importers.user_id = ANY(sqlc.arg(user_ids)::bigint[]) AND importers.requested
 RETURNING importers.invite_id
), counts AS (
 SELECT invite_id, count(*)::integer AS removed_count FROM removed GROUP BY invite_id
), updated AS (
 UPDATE channel_invites AS invites
 SET requested_count = GREATEST(invites.requested_count - counts.removed_count, 0), updated_at = now()
 FROM counts
 WHERE invites.channel_id = sqlc.arg(channel_id) AND invites.invite_id = counts.invite_id
)
SELECT count(*)::integer FROM removed;

-- name: DeleteRevokedChannelInvites :exec
WITH victims AS (
 SELECT invites.channel_id, invites.invite_id FROM channel_invites AS invites
 WHERE invites.channel_id = sqlc.arg(channel_id) AND invites.admin_user_id = sqlc.arg(admin_user_id) AND invites.revoked
 ORDER BY invites.updated_at ASC, invites.invite_id ASC
 LIMIT sqlc.arg('limit')
 FOR UPDATE
), cleared AS (
 DELETE FROM channel_invite_importers AS importers
 USING victims
 WHERE importers.channel_id = victims.channel_id AND importers.invite_id = victims.invite_id AND importers.requested
), deleted AS (
 DELETE FROM channel_invites AS invites
 USING victims
 WHERE invites.channel_id = victims.channel_id AND invites.invite_id = victims.invite_id
 RETURNING invites.hash
)
DELETE FROM channel_invite_hashes AS hashes USING deleted WHERE hashes.hash = deleted.hash;
