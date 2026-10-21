-- name: ListNotifySettingsForOwners :many
-- 推送扇出按页批量读取一组收件人在同一作用域下的通知设置（走主键索引）。
SELECT owner_user_id, show_previews, silent, mute_until, stories_muted, stories_hide_sender
FROM notify_settings
WHERE owner_user_id = ANY(@owner_user_ids::bigint[])
  AND scope_kind = @scope_kind
  AND peer_type = @peer_type
  AND peer_id = @peer_id
  AND topic_id = @topic_id;
