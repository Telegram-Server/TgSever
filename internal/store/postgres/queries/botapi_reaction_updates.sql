-- name: EnqueueBotAPIReactionUpdate :one
WITH inserted AS (
 INSERT INTO bot_api_updates (bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date, reaction_payload)
 SELECT sqlc.arg(bot_user_id), sqlc.arg(update_kind)::varchar(32), sqlc.arg(peer_type)::varchar(16), sqlc.arg(peer_id), sqlc.arg(message_id), 0, sqlc.arg(date), sqlc.arg(reaction_payload)::jsonb
 WHERE EXISTS (SELECT 1 FROM bot_api_update_states WHERE bot_user_id = sqlc.arg(bot_user_id) AND sqlc.arg(update_kind)::text = ANY(allowed_updates))
 RETURNING id
), wake_webhook AS (
 UPDATE bot_api_webhooks SET next_attempt_at = now(), updated_at = now()
 WHERE bot_user_id = sqlc.arg(bot_user_id) AND EXISTS (SELECT 1 FROM inserted)
 RETURNING bot_user_id
)
SELECT id FROM inserted;
