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
INSERT INTO account_device_bindings (user_id, device_id, device_model, platform, app_version)
VALUES ($1, $2, $3, $4, $5);

-- name: ListDeviceBindings :many
SELECT user_id, device_id, device_model, platform, app_version, bound_at, last_used_at
FROM account_device_bindings
WHERE user_id = $1
ORDER BY bound_at, device_id;

-- name: DeleteDeviceBindingsByUser :execrows
-- 只供账号注销：用户是软删除，外键级联不会发生。
DELETE FROM account_device_bindings
WHERE user_id = $1;
