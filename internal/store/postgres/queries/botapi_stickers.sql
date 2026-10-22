-- name: CreateOwnedStickerSet :exec
INSERT INTO sticker_sets (
 id, access_hash, short_name, title, count, hash, set_kind,
 official, animated, videos, emojis, masks, text_color, creator_user_id, creator_bot_id,
 installed, archived, deleted, installed_date,
 thumb_document_id, thumbs, thumb_dc_id, thumb_version,
 document_ids, packs, keywords, sort_order, system_key, software, updated_at
) VALUES (
 @id, @access_hash, @short_name, @title, @count, @hash, @set_kind,
 @official, @animated, @videos, @emojis, @masks, @text_color, @creator_user_id, @creator_bot_id,
 @installed, @archived, @deleted, @installed_date,
 @thumb_document_id, @thumbs::jsonb, @thumb_dc_id, @thumb_version,
 @document_ids::jsonb, @packs::jsonb, @keywords::jsonb, @sort_order, @system_key, @software, now()
);
