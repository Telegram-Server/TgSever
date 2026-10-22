-- name: EnqueueBotAPIChatJoinRequestUpdate :one
-- chat_join_request 属于默认集合：allowed_updates 为 NULL（未设置/清空）时投递，
-- 显式列表不含它时不插入（返回零行）。插入成功时顺带唤醒该机器人的 webhook。
WITH inserted AS (
  INSERT INTO bot_api_updates (
    bot_user_id, update_kind, peer_type, peer_id, message_id, source_pts, date, join_request_payload
  )
  SELECT sqlc.arg(bot_user_id)::bigint, 'chat_join_request', 'channel', sqlc.arg(peer_id)::bigint, 0, 0,
         sqlc.arg(date)::integer, sqlc.arg(join_request_payload)::jsonb
  WHERE NOT EXISTS (
    SELECT 1
    FROM bot_api_update_states
    WHERE bot_api_update_states.bot_user_id = sqlc.arg(bot_user_id)::bigint
      AND bot_api_update_states.allowed_updates IS NOT NULL
      AND NOT ('chat_join_request' = ANY(bot_api_update_states.allowed_updates))
  )
  RETURNING id
), wake_webhook AS (
  UPDATE bot_api_webhooks
  SET next_attempt_at = now(), updated_at = now()
  WHERE bot_api_webhooks.bot_user_id = sqlc.arg(bot_user_id)::bigint AND EXISTS (SELECT 1 FROM inserted)
  RETURNING bot_api_webhooks.bot_user_id
)
SELECT id FROM inserted;
