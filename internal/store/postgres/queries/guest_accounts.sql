-- name: GetGuestIPBinding :one
SELECT ip_key, user_id, bound_at
FROM guest_ip_bindings
WHERE ip_key = $1;

-- name: GetGuestAccount :one
SELECT user_id, phone, ip_key, created_at
FROM guest_accounts
WHERE user_id = $1;

-- name: InsertGuestAccount :exec
INSERT INTO guest_accounts (user_id, phone, ip_key)
VALUES ($1, $2, $3);

-- BindGuestIP 把 IP 键绑定到新账号：没有绑定时插入；已有绑定时只有它仍指向调用方看到的
-- previous_user_id 才替换（比较并交换）。返回 0 行表示别人抢先换了绑定。
-- name: BindGuestIP :execrows
INSERT INTO guest_ip_bindings (ip_key, user_id)
VALUES (sqlc.arg(ip_key), sqlc.arg(user_id))
ON CONFLICT (ip_key) DO UPDATE
SET user_id = EXCLUDED.user_id,
    bound_at = now()
WHERE guest_ip_bindings.user_id = sqlc.narg(previous_user_id)::bigint;
