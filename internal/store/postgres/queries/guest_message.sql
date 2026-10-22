-- name: SetChannelGuestMessageProvenance :exec
UPDATE channel_messages
SET guestchat_via_from = sqlc.arg(guestchat_via_from)::bigint
WHERE channel_id = sqlc.arg(channel_id)::bigint AND id = sqlc.arg(message_id)::int;
