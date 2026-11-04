-- name: AddSavedRingtone :execrows
-- 未满上限才插入；已存在时冲突跳过。0 行=已存在或已满，由调用方再查区分。
INSERT INTO saved_ringtones (user_id, document_id)
SELECT @user_id::bigint, @document_id::bigint
WHERE (SELECT count(*) FROM saved_ringtones WHERE user_id = @user_id::bigint) < @max_count::bigint
ON CONFLICT (user_id, document_id) DO NOTHING;

-- name: SavedRingtoneExists :one
SELECT EXISTS (
    SELECT 1 FROM saved_ringtones WHERE user_id = @user_id AND document_id = @document_id
);

-- name: RemoveSavedRingtone :execrows
DELETE FROM saved_ringtones WHERE user_id = @user_id AND document_id = @document_id;

-- name: ListSavedRingtoneIDs :many
SELECT document_id FROM saved_ringtones WHERE user_id = @user_id ORDER BY seq DESC;
