-- 入群申请发给管理员 bot（#149）。

-- name: GetJoinRequesterAbout :one
SELECT about
FROM users
WHERE id = $1;
