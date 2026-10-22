-- name: SetBotDefaultAdminRights :execrows
UPDATE bots
SET group_admin_rights = CASE WHEN sqlc.arg(broadcast)::boolean THEN group_admin_rights ELSE sqlc.arg(rights)::jsonb END,
    broadcast_admin_rights = CASE WHEN sqlc.arg(broadcast)::boolean THEN sqlc.arg(rights)::jsonb ELSE broadcast_admin_rights END,
    updated_at = now()
WHERE bot_user_id = sqlc.arg(bot_user_id)::bigint
  AND (CASE WHEN sqlc.arg(broadcast)::boolean THEN broadcast_admin_rights ELSE group_admin_rights END) <> sqlc.arg(rights)::jsonb;

-- name: GetBotsByIDs :many
SELECT * FROM bots WHERE bot_user_id = ANY(sqlc.arg(ids)::bigint[]);
