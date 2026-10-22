-- 账号设备绑定（#112）。名额 = 账号的绑定行数；判定与写入都在绑定事务的用户锁下进行。

-- name: GetAuthKeyDeviceID :one
-- auth key 上持久化的 device_id（#111）；NULL（从未上报）读成空串。
SELECT COALESCE(device_id, '')::text AS device_id
FROM auth_keys
WHERE auth_key_id = $1;

-- name: DeviceBindingExists :one
SELECT EXISTS (
  SELECT 1 FROM account_device_bindings
  WHERE user_id = $1 AND device_id = $2
);

-- name: TouchDeviceBinding :execrows
-- 已绑定的设备再次完成登录：只刷新最近使用时间，绑定时的设备信息不变。
UPDATE account_device_bindings
SET last_used_at = now()
WHERE user_id = $1 AND device_id = $2;

-- name: CountDeviceBindings :one
SELECT count(*)
FROM account_device_bindings
WHERE user_id = $1;

-- name: InsertDeviceBinding :exec
-- 用 clock_timestamp() 而不是事务开始时刻：设上限时一次补绑多台设备（#115），名单里的
-- 绑定先后要与登录先后一致。
INSERT INTO account_device_bindings (user_id, device_id, device_model, platform, app_version, bound_at, last_used_at)
VALUES ($1, $2, $3, $4, $5, clock_timestamp(), clock_timestamp());

-- name: ListBoundDeviceIDs :many
SELECT device_id
FROM account_device_bindings
WHERE user_id = $1;

-- name: ListDeviceBackfillCandidates :many
-- 设上限时的补绑候选（#115）：账号已完成（非 password_pending）且 auth key 上报过 device_id
-- 的授权，按登录时间先后。
SELECT k.device_id::text AS device_id, a.device_model, a.platform, a.app_version
FROM authorizations a
JOIN auth_keys k ON k.auth_key_id = a.auth_key_id
WHERE a.user_id = $1
  AND NOT a.password_pending
  AND k.device_id IS NOT NULL
ORDER BY a.created_at, a.auth_key_id;

-- name: GetDeviceBackfillTarget :one
-- 延迟补绑（#115）：auth key 上的已完成授权属于设了上限的非 bot 账号时返回它与 key 上的
-- device_id；其余情况没有行。每次持久化 device_id 都会调用，未受限的账号一条查询就结束。
SELECT a.user_id, COALESCE(k.device_id, '')::text AS device_id, a.device_model, a.platform, a.app_version
FROM authorizations a
JOIN auth_keys k ON k.auth_key_id = a.auth_key_id
JOIN account_restrictions r ON r.user_id = a.user_id
JOIN users u ON u.id = a.user_id
WHERE a.auth_key_id = $1
  AND NOT a.password_pending
  AND r.device_limit IS NOT NULL
  AND NOT u.is_bot;

-- name: ListDeviceBindings :many
SELECT user_id, device_id, device_model, platform, app_version, bound_at, last_used_at
FROM account_device_bindings
WHERE user_id = $1
ORDER BY bound_at, device_id;

-- name: DeleteDeviceBindingsByUser :execrows
-- 只供账号注销：用户是软删除，外键级联不会发生。
DELETE FROM account_device_bindings
WHERE user_id = $1;

-- name: IsBotUser :one
-- 试运行补绑（#115）时排除 bot：bot 不受上限约束，也不补绑。
SELECT is_bot FROM users WHERE id = $1;
