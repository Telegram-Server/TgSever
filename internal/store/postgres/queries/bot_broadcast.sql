-- name: EnsureBotBroadcastWallet :exec
INSERT INTO bot_stars_balances(bot_user_id,balance) VALUES(sqlc.arg(bot_id)::bigint,0) ON CONFLICT(bot_user_id) DO NOTHING;

-- name: LockBotBroadcastWallet :one
SELECT balance,nanos FROM bot_stars_balances WHERE bot_user_id=sqlc.arg(bot_id)::bigint FOR UPDATE;

-- name: SetBotBroadcastBalance :exec
UPDATE bot_stars_balances SET balance=sqlc.arg(amount)::bigint,nanos=sqlc.arg(nanos)::integer,updated_at=now() WHERE bot_user_id=sqlc.arg(bot_id)::bigint;

-- name: GetBotBroadcastReservation :one
SELECT data FROM bot_broadcast_reservations WHERE bot_user_id=sqlc.arg(bot_id)::bigint AND request_key=sqlc.arg(request_key)::text;

-- name: ActiveBotBroadcastReservations :many
SELECT data FROM bot_broadcast_reservations WHERE bot_user_id=sqlc.arg(bot_id)::bigint
 AND (state='reserved' OR (state='committed' AND completed_at>sqlc.arg(since)::timestamptz));

-- name: PutBotBroadcastReservation :exec
INSERT INTO bot_broadcast_reservations(bot_user_id,request_key,state,created_at,completed_at,data)
VALUES(sqlc.arg(bot_id)::bigint,sqlc.arg(request_key)::text,sqlc.arg(state)::text,sqlc.arg(created_at)::timestamptz,sqlc.narg(completed_at)::timestamptz,sqlc.arg(data)::jsonb)
ON CONFLICT(bot_user_id,request_key) DO UPDATE SET state=EXCLUDED.state,created_at=EXCLUDED.created_at,completed_at=EXCLUDED.completed_at,data=EXCLUDED.data;

-- name: BotPaidBroadcastEnabled :one
SELECT COALESCE((SELECT enabled FROM bot_paid_broadcast_settings WHERE bot_user_id=sqlc.arg(bot_id)::bigint),false)::boolean;

-- name: SetBotPaidBroadcastEnabled :exec
INSERT INTO bot_paid_broadcast_settings(bot_user_id,enabled) VALUES(sqlc.arg(bot_id)::bigint,sqlc.arg(enabled)::boolean)
ON CONFLICT(bot_user_id) DO UPDATE SET enabled=EXCLUDED.enabled;

-- name: InsertBotBroadcastTransaction :exec
INSERT INTO bot_stars_transactions(bot_user_id,actor_user_id,amount,nanos,reason,invoice_key,date)
VALUES(sqlc.arg(bot_id)::bigint,sqlc.arg(bot_id)::bigint,sqlc.arg(amount)::bigint,sqlc.arg(nanos)::integer,'paid_broadcast',sqlc.arg(request_key)::text,sqlc.arg(date)::integer);

-- name: ExpiredBotBroadcastReservations :many
SELECT data FROM bot_broadcast_reservations WHERE bot_user_id=sqlc.arg(bot_id)::bigint
 AND state='reserved' AND created_at<=sqlc.arg(deadline)::timestamptz;
