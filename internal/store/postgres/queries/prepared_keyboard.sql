-- name: SaveBotPreparedKeyboardButton :exec
INSERT INTO webview_requested_buttons (
 webapp_req_id,bot_user_id,user_id,button_id,text,peer_type,max_quantity,peer_filter,
 name_requested,username_requested,photo_requested,created_at,expires_at,
 style,icon_custom_emoji_id,suggested_name,suggested_username
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17);

-- name: GetBotPreparedKeyboardButton :one
SELECT * FROM webview_requested_buttons
WHERE bot_user_id=$1 AND user_id=$2 AND webapp_req_id=$3 AND expires_at>now();

-- name: ConsumeBotPreparedKeyboardButton :one
DELETE FROM webview_requested_buttons
WHERE bot_user_id=$1 AND user_id=$2 AND webapp_req_id=$3 AND expires_at>now()
RETURNING *;

-- name: DeleteBotPreparedKeyboardButton :exec
DELETE FROM webview_requested_buttons WHERE bot_user_id=$1 AND user_id=$2 AND webapp_req_id=$3;

-- name: PruneBotPreparedKeyboardButtons :exec
DELETE FROM webview_requested_buttons WHERE expires_at<=now();
