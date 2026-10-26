-- name: LockBotForCommandSets :one
-- 串行化同一 bot 的桶写入，让「数桶数 → 插入」的上限检查不被并发写穿透。
SELECT bot_user_id FROM bots WHERE bot_user_id = $1 FOR UPDATE;

-- name: GetBotCommandSet :one
SELECT commands FROM bot_command_sets
WHERE bot_user_id = $1 AND scope = $2 AND peer_type = $3 AND peer_id = $4 AND user_id = $5 AND lang_code = $6;

-- name: CountBotCommandSets :one
SELECT count(*) FROM bot_command_sets WHERE bot_user_id = $1;

-- name: UpsertBotCommandSet :exec
INSERT INTO bot_command_sets (bot_user_id, scope, peer_type, peer_id, user_id, lang_code, commands)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (bot_user_id, scope, peer_type, peer_id, user_id, lang_code)
DO UPDATE SET commands = EXCLUDED.commands, updated_at = now();

-- name: DeleteBotCommandSet :execrows
DELETE FROM bot_command_sets
WHERE bot_user_id = $1 AND scope = $2 AND peer_type = $3 AND peer_id = $4 AND user_id = $5 AND lang_code = $6;

-- name: ListBotCommandSetsForPeer :many
-- 一组 bot 在某个会话里可能用到的全部桶：不绑会话的全局桶，加上绑在这个会话上的桶；
-- peer_user 只取查看者自己的。
SELECT bot_user_id, scope, peer_type, peer_id, user_id, lang_code, commands
FROM bot_command_sets
WHERE bot_user_id = ANY(sqlc.arg(bot_user_ids)::bigint[])
  AND (
    peer_id = 0 OR
    (peer_type = sqlc.arg(peer_type)::text AND peer_id = sqlc.arg(peer_id)::bigint
     AND (scope <> 'peer_user' OR user_id = sqlc.arg(viewer_user_id)::bigint))
  );
