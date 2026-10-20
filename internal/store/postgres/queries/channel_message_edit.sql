-- name: LockChannelForMessageEdit :one
SELECT id
FROM channels
WHERE id = sqlc.arg(channel_id)
FOR UPDATE;
