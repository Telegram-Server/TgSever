-- name: InsertScheduledMessage :one
-- 定时消息查询组读出一律返回整行（sqlcgen.ScheduledMessage），由 scheduledMessageFromRow
-- 统一解码，新增列只需改迁移与解码一处。
INSERT INTO scheduled_messages (
  owner_user_id, scheduled_id, peer_type, peer_id, random_id, message_date,
  body, entities, media, rich_message, silent, noforwards,
  reply_to_msg_id, reply_to_peer_type, reply_to_peer_id, reply_to_top_id,
  quote_text, quote_entities, quote_offset,
  fwd_from_peer_type, fwd_from_peer_id, fwd_from_name, fwd_date,
  send_as_peer_type, send_as_peer_id, grouped_id,
  schedule_date, schedule_repeat_period, state, created_at, updated_at
) VALUES (
  sqlc.arg(owner_user_id), sqlc.arg(scheduled_id), sqlc.arg(peer_type), sqlc.arg(peer_id), sqlc.arg(random_id), sqlc.arg(message_date),
  sqlc.arg(body), sqlc.arg(entities)::jsonb, sqlc.arg(media)::jsonb, sqlc.arg(rich_message)::jsonb, sqlc.arg(silent), sqlc.arg(noforwards),
  sqlc.arg(reply_to_msg_id), sqlc.arg(reply_to_peer_type), sqlc.arg(reply_to_peer_id), sqlc.arg(reply_to_top_id),
  sqlc.arg(quote_text), sqlc.arg(quote_entities)::jsonb, sqlc.arg(quote_offset),
  sqlc.arg(fwd_from_peer_type), sqlc.arg(fwd_from_peer_id), sqlc.arg(fwd_from_name), sqlc.arg(fwd_date),
  sqlc.arg(send_as_peer_type), sqlc.arg(send_as_peer_id), sqlc.arg(grouped_id),
  sqlc.arg(schedule_date), sqlc.arg(schedule_repeat_period), 'pending', sqlc.arg(message_date), sqlc.arg(message_date)
)
RETURNING *;

-- name: NextScheduledMessageID :one
SELECT (COALESCE(MAX(scheduled_id), 0) + 1)::int AS next_id
FROM scheduled_messages
WHERE owner_user_id = sqlc.arg(owner_user_id);

-- name: GetPendingScheduledMessageByRandomID :one
SELECT *
FROM scheduled_messages
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND random_id = sqlc.arg(random_id)
  AND random_id <> 0
  AND state IN ('pending', 'dispatching')
ORDER BY scheduled_id ASC
LIMIT 1;

-- name: GetPendingScheduledMessageForEdit :one
SELECT *
FROM scheduled_messages
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND peer_type = sqlc.arg(peer_type)
  AND peer_id = sqlc.arg(peer_id)
  AND scheduled_id = sqlc.arg(scheduled_id)
  AND state = 'pending'
FOR UPDATE;

-- name: UpdateScheduledMessageContent :one
UPDATE scheduled_messages
SET body = sqlc.arg(body),
    entities = sqlc.arg(entities)::jsonb,
    rich_message = sqlc.arg(rich_message)::jsonb,
    schedule_date = sqlc.arg(schedule_date),
    updated_at = sqlc.arg(updated_at)
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND peer_type = sqlc.arg(peer_type)
  AND peer_id = sqlc.arg(peer_id)
  AND scheduled_id = sqlc.arg(scheduled_id)
  AND state = 'pending'
RETURNING *;

-- name: ListScheduledMessagesByPeer :many
SELECT *
FROM scheduled_messages
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND peer_type = sqlc.arg(peer_type)
  AND peer_id = sqlc.arg(peer_id)
  AND state IN ('pending', 'dispatching')
ORDER BY schedule_date ASC, scheduled_id ASC
LIMIT sqlc.arg(limit_count);

-- name: ListScheduledMessagesByIDs :many
SELECT *
FROM scheduled_messages
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND peer_type = sqlc.arg(peer_type)
  AND peer_id = sqlc.arg(peer_id)
  AND scheduled_id = ANY(sqlc.arg(ids)::int[])
  AND state IN ('pending', 'dispatching')
ORDER BY schedule_date ASC, scheduled_id ASC
LIMIT sqlc.arg(limit_count);

-- name: MarkScheduledMessagesDeleted :many
UPDATE scheduled_messages
SET state = 'deleted',
    updated_at = sqlc.arg(updated_at)
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND peer_type = sqlc.arg(peer_type)
  AND peer_id = sqlc.arg(peer_id)
  AND scheduled_id = ANY(sqlc.arg(ids)::int[])
  AND state IN ('pending', 'dispatching')
RETURNING *;

-- name: ClaimScheduledMessagesByIDs :many
UPDATE scheduled_messages
SET state = 'dispatching',
    lease_until = sqlc.arg(lease_until),
    updated_at = sqlc.arg(now_unix),
    last_error = ''
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND peer_type = sqlc.arg(peer_type)
  AND peer_id = sqlc.arg(peer_id)
  AND scheduled_id = ANY(sqlc.arg(ids)::int[])
  AND (state = 'pending' OR (state = 'dispatching' AND lease_until <= sqlc.arg(now_unix)))
RETURNING *;

-- name: ClaimDueScheduledMessages :many
-- retry_floor 让上一次投递失败（last_error 非空）的 pending 行至少退避再被认领；
-- 同时回收 lease 已过期却仍卡在 'dispatching' 的行（派发器崩溃遗留）。
WITH due AS (
  SELECT d.owner_user_id, d.scheduled_id
  FROM scheduled_messages d
  WHERE d.schedule_date <= sqlc.arg(now_unix)::int
    AND (
      (d.state = 'pending' AND (d.last_error = '' OR d.updated_at <= sqlc.arg(retry_floor)::int))
      OR (d.state = 'dispatching' AND d.lease_until <= sqlc.arg(now_unix)::int)
    )
  ORDER BY d.schedule_date ASC, d.owner_user_id ASC, d.scheduled_id ASC
  LIMIT sqlc.arg(limit_count)
  FOR UPDATE SKIP LOCKED
)
UPDATE scheduled_messages sm
SET state = 'dispatching',
    lease_until = sqlc.arg(now_unix)::int + sqlc.arg(lease_seconds)::int,
    updated_at = sqlc.arg(now_unix)::int,
    last_error = ''
FROM due
WHERE sm.owner_user_id = due.owner_user_id
  AND sm.scheduled_id = due.scheduled_id
RETURNING sm.*;

-- name: MarkScheduledMessageSent :one
UPDATE scheduled_messages
SET state = 'sent',
    sent_message_id = sqlc.arg(sent_message_id),
    lease_until = 0,
    updated_at = sqlc.arg(updated_at)
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND scheduled_id = sqlc.arg(scheduled_id)
RETURNING peer_type, peer_id;

-- name: ReleaseScheduledMessage :exec
UPDATE scheduled_messages
SET state = 'pending',
    lease_until = 0,
    last_error = sqlc.arg(last_error),
    updated_at = EXTRACT(EPOCH FROM now())::int
WHERE owner_user_id = sqlc.arg(owner_user_id)
  AND scheduled_id = sqlc.arg(scheduled_id)
  AND state = 'dispatching';

-- name: HasActiveScheduledMessages :one
SELECT EXISTS (
  SELECT 1
  FROM scheduled_messages
  WHERE owner_user_id = sqlc.arg(owner_user_id)
    AND peer_type = sqlc.arg(peer_type)
    AND peer_id = sqlc.arg(peer_id)
    AND state IN ('pending', 'dispatching')
)::boolean AS has_scheduled;

-- name: UpsertPrivateDialogHasScheduled :exec
INSERT INTO dialogs (user_id, peer_type, peer_id, has_scheduled)
VALUES (sqlc.arg(user_id), 'user', sqlc.arg(peer_id), sqlc.arg(has_scheduled))
ON CONFLICT (user_id, peer_type, peer_id) DO UPDATE SET
  has_scheduled = EXCLUDED.has_scheduled,
  updated_at = now();

-- name: SetChannelDialogHasScheduled :exec
UPDATE channel_dialogs
SET has_scheduled = sqlc.arg(has_scheduled),
    updated_at = now()
WHERE user_id = sqlc.arg(user_id)
  AND channel_id = sqlc.arg(channel_id);
