-- name: SetChannelStickerSet :exec
UPDATE channels SET sticker_set_id = $2, updated_at = now() WHERE id = $1;
