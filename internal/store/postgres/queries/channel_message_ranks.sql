-- name: GetChannelMessageRanks :many
SELECT m.channel_id, m.user_id, m.rank
FROM unnest(sqlc.arg(channel_ids)::bigint[]) WITH ORDINALITY AS c(channel_id, ord)
JOIN unnest(sqlc.arg(user_ids)::bigint[]) WITH ORDINALITY AS u(user_id, ord) USING (ord)
JOIN channel_members AS m ON m.channel_id = c.channel_id AND m.user_id = u.user_id
WHERE m.status = 'active' AND m.rank <> '';
