-- name: EnqueueBotAPIJoinRequestUpdate :one
WITH inserted AS (
 INSERT INTO bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date, join_request_payload)
 SELECT sqlc.arg(bot_user_id), 'chat_join_request', 'channel', sqlc.arg(peer_id), 0, 0, sqlc.arg(date), sqlc.arg(join_request_payload)::jsonb
 WHERE NOT EXISTS (SELECT 1 FROM bot_api_update_states WHERE bot_user_id = sqlc.arg(bot_user_id) AND allowed_updates IS NOT NULL AND NOT ('chat_join_request' = ANY(allowed_updates)))
 RETURNING id
), wake_webhook AS (
 UPDATE bot_api_webhooks SET next_attempt_at = now(), updated_at = now()
 WHERE bot_user_id = sqlc.arg(bot_user_id) AND EXISTS (SELECT 1 FROM inserted)
 RETURNING bot_user_id
)
SELECT id FROM inserted;
