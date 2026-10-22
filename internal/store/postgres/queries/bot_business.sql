-- name: LockBusinessConnectionOwner :one
SELECT id FROM users WHERE id = $1 FOR UPDATE;

-- name: GetCurrentBusinessConnection :one
SELECT c.snapshot FROM business_connected_bots b JOIN business_bot_connections c USING (connection_id) WHERE b.owner_user_id = $1;

-- name: GetBusinessBotConnection :one
SELECT snapshot FROM business_bot_connections WHERE connection_id = $1;

-- name: SaveCurrentBusinessConnection :one
INSERT INTO business_connected_bots(owner_user_id,bot_user_id,recipients,rights,connection_id,created_at,updated_at)
VALUES($1,$2,$3,$4,$5,now(),now())
ON CONFLICT(owner_user_id) DO UPDATE SET
    bot_user_id = EXCLUDED.bot_user_id, recipients = EXCLUDED.recipients, rights = EXCLUDED.rights,
    connection_id = CASE WHEN business_connected_bots.bot_user_id = EXCLUDED.bot_user_id THEN business_connected_bots.connection_id ELSE EXCLUDED.connection_id END,
    created_at = CASE WHEN business_connected_bots.bot_user_id = EXCLUDED.bot_user_id THEN business_connected_bots.created_at ELSE now() END,
    updated_at = now()
RETURNING connection_id, extract(epoch from created_at)::bigint AS created_at_unix, extract(epoch from updated_at)::bigint AS updated_at_unix;

-- name: RetireOtherBusinessConnections :exec
UPDATE business_bot_connections SET snapshot = jsonb_set(snapshot, '{Disabled}', 'true'), updated_at = now()
WHERE owner_user_id = $1 AND connection_id <> $2 AND NOT (snapshot->>'Disabled')::boolean;

-- name: SaveBusinessConnectionSnapshot :exec
INSERT INTO business_bot_connections(connection_id,owner_user_id,bot_user_id,snapshot) VALUES($1,$2,$3,$4)
ON CONFLICT(connection_id) DO UPDATE SET snapshot = EXCLUDED.snapshot, updated_at = now();

-- name: RevokeBusinessBotConnection :execrows
UPDATE business_bot_connections SET snapshot = jsonb_set(snapshot, '{Disabled}', 'true'), updated_at = now()
WHERE connection_id IN (SELECT current_bot.connection_id FROM business_connected_bots AS current_bot WHERE current_bot.owner_user_id = $1 AND current_bot.bot_user_id = $2);

-- name: DeleteCurrentBusinessConnection :exec
DELETE FROM business_connected_bots WHERE owner_user_id = $1 AND bot_user_id = $2;

-- name: ClearBusinessConnectionPeerStates :exec
DELETE FROM business_connected_bot_peer_states WHERE owner_user_id = $1;

-- name: DeleteBusinessConnectionsForUser :exec
WITH clear_states AS (
    DELETE FROM business_connected_bot_peer_states
    WHERE owner_user_id = sqlc.arg(user_id) OR peer_user_id = sqlc.arg(user_id)
       OR owner_user_id IN (SELECT owner_user_id FROM business_connected_bots WHERE bot_user_id = sqlc.arg(user_id))
), clear_current AS (
    DELETE FROM business_connected_bots WHERE owner_user_id = sqlc.arg(user_id) OR bot_user_id = sqlc.arg(user_id)
)
DELETE FROM business_bot_connections AS history WHERE history.owner_user_id = sqlc.arg(user_id) OR history.bot_user_id = sqlc.arg(user_id);
