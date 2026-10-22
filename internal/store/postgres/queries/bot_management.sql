-- name: GetBotManagement :one
SELECT b.bot_user_id, COALESCE(m.manager_bot_id, 0)::bigint AS manager_bot_id,
 COALESCE(m.is_access_restricted, false)::boolean AS is_access_restricted,
 COALESCE(m.added_user_ids, '{}'::bigint[])::bigint[] AS added_user_ids
FROM bots b LEFT JOIN bot_management m ON m.bot_user_id=b.bot_user_id
WHERE b.bot_user_id=$1;

-- name: SetBotManager :execrows
INSERT INTO bot_management(bot_user_id,manager_bot_id)
SELECT b.bot_user_id, sqlc.arg(manager_id)::bigint FROM bots b
WHERE b.bot_user_id=sqlc.arg(bot_id) AND b.owner_user_id=sqlc.arg(owner_id)
AND EXISTS(SELECT 1 FROM bots manager WHERE manager.bot_user_id=sqlc.arg(manager_id) AND manager.can_manage_bots)
AND b.bot_user_id<>sqlc.arg(manager_id)
ON CONFLICT(bot_user_id) DO UPDATE SET manager_bot_id=EXCLUDED.manager_bot_id;

-- name: SetManagedBotAccess :execrows
INSERT INTO bot_management(bot_user_id,is_access_restricted,added_user_ids)
SELECT b.bot_user_id, sqlc.arg(restricted)::boolean, sqlc.arg(added)::bigint[] FROM bots b
LEFT JOIN bot_management m ON m.bot_user_id=b.bot_user_id
WHERE b.bot_user_id=sqlc.arg(bot_id) AND (b.owner_user_id=sqlc.arg(actor_id) OR m.manager_bot_id=sqlc.arg(actor_id))
ON CONFLICT(bot_user_id) DO UPDATE SET is_access_restricted=EXCLUDED.is_access_restricted,added_user_ids=EXCLUDED.added_user_ids;

-- name: LockBotInstanceOwner :one
SELECT bot_user_id FROM bots WHERE bot_user_id=$1 FOR UPDATE;

-- name: GetBotAPICloudLogout :one
SELECT COALESCE(EXTRACT(EPOCH FROM max(logged_out_at)),0)::bigint AS logged_out_at FROM bot_api_instances WHERE bot_user_id=$1;

-- name: GetBotAPIInstance :one
SELECT bot_user_id,instance_id,cloud, EXTRACT(EPOCH FROM started_at)::bigint AS started_at,
 COALESCE(EXTRACT(EPOCH FROM closed_at),0)::bigint AS closed_at,
 COALESCE(EXTRACT(EPOCH FROM logged_out_at),0)::bigint AS logged_out_at
FROM bot_api_instances WHERE bot_user_id=$1 AND instance_id=$2;

-- name: InsertBotAPIInstance :exec
INSERT INTO bot_api_instances(bot_user_id,instance_id,cloud,started_at)
VALUES(sqlc.arg(bot_user_id),sqlc.arg(instance_id),sqlc.arg(cloud),to_timestamp(sqlc.arg(now_unix)::bigint));

-- name: StopBotAPIInstance :exec
UPDATE bot_api_instances SET closed_at=to_timestamp(sqlc.arg(now_unix)::bigint),
 logged_out_at=CASE WHEN sqlc.arg(logout)::boolean AND cloud THEN to_timestamp(sqlc.arg(now_unix)::bigint) ELSE logged_out_at END
WHERE bot_user_id=sqlc.arg(bot_user_id) AND instance_id=sqlc.arg(instance_id);

-- name: SetCanManageBots :execrows
UPDATE bots SET can_manage_bots=sqlc.arg(enabled)::boolean WHERE bot_user_id=sqlc.arg(bot_id) AND owner_user_id=sqlc.arg(owner_id);
