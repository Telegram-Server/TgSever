-- name: CreateBotGuestQuery :exec
INSERT INTO bot_guest_queries (id, bot_user_id, caller_user_id, peer_type, peer_id, message_id, created_at, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8);

-- name: ConsumeBotGuestQuery :one
DELETE FROM bot_guest_queries WHERE id=$1 AND bot_user_id=$2 AND expires_at>$3
RETURNING id, bot_user_id, caller_user_id, peer_type, peer_id, message_id, created_at, expires_at;

-- name: PruneBotGuestQueries :exec
DELETE FROM bot_guest_queries WHERE expires_at <= $1;

-- name: GetBotGuestQuery :one
SELECT * FROM bot_guest_queries WHERE id=$1 AND bot_user_id=$2 AND expires_at>$3;

-- name: GuestBotEnabled :one
SELECT COALESCE((SELECT guest_chat FROM bots WHERE bot_user_id=$1),false)::boolean;

-- name: SetGuestBotEnabled :exec
UPDATE bots SET guest_chat=sqlc.arg(enabled) WHERE bot_user_id=sqlc.arg(bot_user_id);

-- name: GetBotInlineFeedback :one
SELECT COALESCE((SELECT inline_feedback_percentage FROM bots WHERE bot_user_id=$1),0)::integer;

-- name: SetBotInlineFeedback :exec
UPDATE bots SET inline_feedback_percentage=sqlc.arg(percentage) WHERE bot_user_id=sqlc.arg(bot_user_id);
