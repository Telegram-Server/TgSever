-- name: CountChannelInviteImporters :one
SELECT COUNT(*)::integer
FROM channel_invite_importers AS i
JOIN users AS u ON u.id = i.user_id
WHERE i.channel_id = sqlc.arg(channel_id)
  AND i.requested = sqlc.arg(requested)
  AND (sqlc.arg(invite_id)::bigint = 0 OR i.invite_id = sqlc.arg(invite_id))
  AND (sqlc.arg(query)::text = '' OR lower(trim(u.username || ' ' || u.first_name || ' ' || u.last_name)) LIKE '%' || lower(sqlc.arg(query)::text) || '%')
  AND ((sqlc.arg(offset_date)::integer = 0 AND sqlc.arg(offset_user_id)::bigint = 0) OR (i.date, i.user_id) < (sqlc.arg(offset_date)::integer, sqlc.arg(offset_user_id)::bigint))
  AND (sqlc.arg(owner_filter)::bigint = 0 OR EXISTS (
      SELECT 1 FROM channel_invites AS owned
      WHERE owned.channel_id = i.channel_id AND owned.invite_id = i.invite_id
        AND owned.admin_user_id = sqlc.arg(owner_filter)::bigint
  ));

-- name: ListChannelInviteImporters :many
SELECT i.channel_id, i.invite_id, i.user_id, i.date, i.requested, i.approved_by, i.via_chatlist, i.about
FROM channel_invite_importers AS i
JOIN users AS u ON u.id = i.user_id
WHERE i.channel_id = sqlc.arg(channel_id)
  AND i.requested = sqlc.arg(requested)
  AND (sqlc.arg(invite_id)::bigint = 0 OR i.invite_id = sqlc.arg(invite_id))
  AND (sqlc.arg(query)::text = '' OR lower(trim(u.username || ' ' || u.first_name || ' ' || u.last_name)) LIKE '%' || lower(sqlc.arg(query)::text) || '%')
  AND ((sqlc.arg(offset_date)::integer = 0 AND sqlc.arg(offset_user_id)::bigint = 0) OR (i.date, i.user_id) < (sqlc.arg(offset_date)::integer, sqlc.arg(offset_user_id)::bigint))
  AND (sqlc.arg(owner_filter)::bigint = 0 OR EXISTS (
      SELECT 1 FROM channel_invites AS owned
      WHERE owned.channel_id = i.channel_id AND owned.invite_id = i.invite_id
        AND owned.admin_user_id = sqlc.arg(owner_filter)::bigint
  ))
ORDER BY i.date DESC, i.user_id DESC
LIMIT sqlc.arg(page_limit)::integer;
