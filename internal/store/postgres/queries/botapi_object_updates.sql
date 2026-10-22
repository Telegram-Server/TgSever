-- name: EnqueueBotAPIObjectUpdate :one
WITH inserted AS (
 INSERT INTO bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date, object_payload)
 SELECT sqlc.arg(bot_user_id), sqlc.arg(update_kind)::varchar(32), '', 0, 0, 0, sqlc.arg(date), sqlc.arg(object_payload)::jsonb
 WHERE NOT EXISTS (
  SELECT 1 FROM bot_api_update_states WHERE bot_user_id=sqlc.arg(bot_user_id) AND allowed_updates IS NOT NULL AND NOT (sqlc.arg(update_kind)::text=ANY(allowed_updates))
 )
 RETURNING id
), wake_webhook AS (
 UPDATE bot_api_webhooks SET next_attempt_at=now(), updated_at=now()
 WHERE bot_user_id=sqlc.arg(bot_user_id) AND EXISTS (SELECT 1 FROM inserted) RETURNING bot_user_id
)
SELECT id FROM inserted;
