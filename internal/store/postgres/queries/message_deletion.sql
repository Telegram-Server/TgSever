-- name: InsertPrivateMessageDeletions :exec
-- 在删除事务内为一批被删的 message box 写删除记录，同一调用共享一个 batch_id。
WITH batch AS (
  SELECT nextval('message_deletion_batch_seq') AS id
),
-- 删除时刻的设备快照：授权行之后可能因退出登录而消失。
device AS (
  SELECT COALESCE((
    SELECT jsonb_build_object('device_model', a.device_model, 'platform', a.platform, 'ip', a.ip)
    FROM authorizations a
    WHERE a.auth_key_id = sqlc.arg(origin_auth_key_id)::bigint
  ), '{}'::jsonb) AS info
),
targets AS (
  SELECT o.owner_user_id, b.box_id
  FROM unnest(sqlc.arg(owner_user_ids)::bigint[]) WITH ORDINALITY AS o(owner_user_id, ord)
  JOIN unnest(sqlc.arg(box_ids)::int[]) WITH ORDINALITY AS b(box_id, ord) USING (ord)
)
INSERT INTO message_deletions (
  batch_id, deleted_date, source, scope, actor_type, actor_user_id,
  origin_auth_key_id, origin_session_id, origin_device,
  operator_id, command_id, reason,
  owner_user_id, peer_type, peer_id, message_id, private_message_id,
  sender_user_id, message_date, snapshot
)
SELECT
  batch.id,
  sqlc.arg(deleted_date)::int,
  sqlc.arg(source)::text,
  sqlc.arg(scope)::text,
  sqlc.arg(actor_type)::text,
  sqlc.arg(actor_user_id)::bigint,
  sqlc.arg(origin_auth_key_id)::bigint,
  sqlc.arg(origin_session_id)::bigint,
  device.info,
  sqlc.arg(operator_id)::text,
  sqlc.arg(command_id)::text,
  sqlc.arg(reason)::text,
  m.owner_user_id,
  m.peer_type,
  m.peer_id,
  m.box_id,
  m.private_message_id,
  m.message_sender_id,
  m.message_date,
  row_to_json(m)::jsonb
FROM targets
JOIN message_boxes m
  ON m.owner_user_id = targets.owner_user_id
 AND m.box_id = targets.box_id
CROSS JOIN batch
CROSS JOIN device
ORDER BY m.owner_user_id, m.box_id;

-- name: ListMessageDeletions :many
SELECT
  id, batch_id, deleted_date, source, scope, actor_type, actor_user_id,
  owner_user_id, peer_type, peer_id, message_id, sender_user_id, message_date,
  COALESCE(snapshot->>'body', '')::text AS body,
  origin_auth_key_id, origin_session_id,
  operator_id, command_id, reason,
  COALESCE(origin_device->>'device_model', '')::text AS origin_device_model,
  COALESCE(origin_device->>'platform', '')::text AS origin_platform,
  COALESCE(origin_device->>'ip', '')::text AS origin_ip
FROM message_deletions
WHERE (sqlc.arg(actor_user_id)::bigint = 0 OR actor_user_id = sqlc.arg(actor_user_id)::bigint)
  AND (sqlc.arg(sender_user_id)::bigint = 0 OR sender_user_id = sqlc.arg(sender_user_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit)::int;
