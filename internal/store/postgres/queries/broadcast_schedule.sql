-- name: CountBroadcastSchedules :one
SELECT count(*) FROM broadcast_schedules;

-- name: InsertBroadcastSchedule :one
INSERT INTO broadcast_schedules (
  message, timezone, time_of_day, weekdays, enabled, next_run_at, created_by, updated_by
) VALUES (
  sqlc.arg(message), sqlc.arg(timezone), sqlc.arg(time_of_day), sqlc.arg(weekdays),
  sqlc.arg(enabled), sqlc.narg(next_run_at), sqlc.arg(actor), sqlc.arg(actor)
)
RETURNING *;

-- name: UpdateBroadcastSchedule :one
UPDATE broadcast_schedules
SET message = sqlc.arg(message),
    timezone = sqlc.arg(timezone),
    time_of_day = sqlc.arg(time_of_day),
    weekdays = sqlc.arg(weekdays),
    enabled = sqlc.arg(enabled),
    next_run_at = sqlc.narg(next_run_at),
    updated_by = sqlc.arg(actor),
    updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteBroadcastSchedule :execrows
DELETE FROM broadcast_schedules WHERE id = $1;

-- name: GetBroadcastSchedule :one
SELECT * FROM broadcast_schedules WHERE id = $1;

-- name: ListBroadcastSchedules :many
SELECT * FROM broadcast_schedules ORDER BY id DESC LIMIT $1;

-- name: LockDueBroadcastSchedule :one
-- 一次锁一条到期计划；SKIP LOCKED 让多个实例各取各的，不会两边都发。
SELECT * FROM broadcast_schedules
WHERE enabled AND next_run_at <= sqlc.arg(now)
ORDER BY next_run_at, id
FOR UPDATE SKIP LOCKED
LIMIT 1;

-- name: BroadcastScheduleHasUnfinishedBroadcast :one
-- 上一次的广播是否还在枚举收件人或投递中（与后台列表的“进行中”判定一致）。
SELECT EXISTS (
  SELECT 1 FROM broadcasts
  WHERE schedule_id = $1
    AND (NOT enumeration_done OR sent_count + failed_count < materialized_count)
);

-- name: SnapshotAllBroadcastRecipients :one
SELECT COALESCE(max(id), 0)::bigint AS max_user_id, count(*) AS recipient_count
FROM users
WHERE NOT is_bot
  AND deleted_at IS NULL
  AND id <> ALL(sqlc.arg(excluded_user_ids)::bigint[]);

-- name: InsertScheduledBroadcast :one
INSERT INTO broadcasts (
  message, entities, target_mode, snapshot_max_user_id, enumeration_done,
  target_count, created_by, schedule_id
) VALUES (
  sqlc.arg(message), sqlc.arg(entities)::jsonb, 'all', sqlc.arg(snapshot_max_user_id), false,
  sqlc.arg(target_count), sqlc.arg(created_by), sqlc.arg(schedule_id)
)
RETURNING id;

-- name: MarkBroadcastScheduleFired :exec
UPDATE broadcast_schedules
SET next_run_at = sqlc.narg(next_run_at),
    enabled = sqlc.narg(next_run_at)::timestamptz IS NOT NULL,
    last_run_at = sqlc.arg(fired_at),
    last_broadcast_id = sqlc.arg(broadcast_id),
    run_count = run_count + 1
WHERE id = sqlc.arg(id);

-- name: MarkBroadcastScheduleSkipped :exec
UPDATE broadcast_schedules
SET next_run_at = sqlc.narg(next_run_at),
    enabled = sqlc.narg(next_run_at)::timestamptz IS NOT NULL,
    last_skipped_at = sqlc.arg(skipped_at),
    last_skip_reason = sqlc.arg(reason),
    skipped_count = skipped_count + 1
WHERE id = sqlc.arg(id);
