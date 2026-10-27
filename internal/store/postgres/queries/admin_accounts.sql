-- 管理面板（cmd/telesrv-admin）的账号只读投影：账号搜索与发送白名单成员（#160）。

-- name: AdminSearchAccounts :many
-- 精确匹配（rank 0：id、两种写法的号码、@用户名，包括 peer_usernames 里的可编辑行）与名字包含
-- 匹配（rank 1）分支各自走索引：名字分支的表达式与 0001_init 的 users_name_lower_trgm_idx 一致
-- （lower(btrim(名 || ' ' || 姓))）。name_pattern 为空串时不做名字匹配。
-- display_username 与账号列表一致：可编辑槽位优先，其次 peer_usernames 里的可编辑行。
WITH matched AS (
	SELECT m.id, min(m.rank)::int AS rank
	FROM (
		SELECT users.id, 0 AS rank FROM users
		WHERE users.id = sqlc.arg(id)::bigint OR users.phone = sqlc.arg(phone)::text
			OR users.phone = sqlc.arg(phone_raw)::text OR lower(users.username) = sqlc.arg(username)::text
		UNION ALL
		SELECT peer_usernames.peer_id, 0 FROM peer_usernames
		WHERE peer_usernames.peer_type = 'user' AND peer_usernames.editable
			AND peer_usernames.username_lower = sqlc.arg(username)::text
		UNION ALL
		SELECT users.id, 1 FROM users
		WHERE sqlc.arg(name_pattern)::text <> ''
			AND lower(btrim(users.first_name || ' ' || users.last_name)) LIKE lower(sqlc.arg(name_pattern)::text)
	) m
	GROUP BY m.id
),
auth AS (
	SELECT authorizations.user_id, max(authorizations.active_at)::timestamptz AS last_active_at, count(*)::int AS device_count
	FROM authorizations
	WHERE authorizations.user_id IN (SELECT matched.id FROM matched)
	GROUP BY authorizations.user_id
)
SELECT u.id, u.phone, u.first_name, u.last_name, u.created_at, u.updated_at,
	COALESCE(r.frozen, false)::boolean AS frozen, COALESCE(r.reason, '')::text AS reason, u.verified, u.scam, u.fake,
	COALESCE(EXTRACT(EPOCH FROM u.premium_expires_at), 0)::bigint AS premium_until,
	COALESCE(a.last_active_at, '0001-01-01 00:00:00+00'::timestamptz)::timestamptz AS last_active_at,
	COALESCE(a.device_count, 0)::int AS device_count,
	COALESCE(NULLIF(u.username, ''), p.username_lower, '')::text AS display_username, u.is_bot
FROM matched m
JOIN users u ON u.id = m.id
LEFT JOIN account_restrictions r ON r.user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
LEFT JOIN auth a ON a.user_id = u.id
ORDER BY m.rank, u.id
LIMIT sqlc.arg(row_limit);

-- name: AdminWhitelistMembers :many
-- 一组账号的展示信息；不存在的 id 没有行，由调用方补上。用户名取法与 AdminSearchAccounts 一致。
SELECT u.id, u.first_name, u.last_name,
	COALESCE(NULLIF(u.username, ''), p.username_lower, '')::text AS display_username, u.phone, u.is_bot
FROM users u
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
WHERE u.id = ANY(sqlc.arg(ids)::bigint[]);
