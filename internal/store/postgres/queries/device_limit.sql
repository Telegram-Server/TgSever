-- 账号设备上限（#105）。上限存在 account_restrictions.device_limit，NULL = 不限制。

-- name: GetDeviceLimit :one
SELECT device_limit
FROM account_restrictions
WHERE user_id = $1 AND device_limit IS NOT NULL;

-- name: SetDeviceLimit :exec
-- 只写 device_limit：冻结相关的列、version 与 updated_at 属于冻结状态，不跟着变。新插入的行
-- 只为上限而生，freeze_recorded = false，冻结读路径不把它当成冻结记录。
INSERT INTO account_restrictions (user_id, device_limit, freeze_recorded)
VALUES ($1, $2, false)
ON CONFLICT (user_id) DO UPDATE
SET device_limit = EXCLUDED.device_limit;

-- name: ClearDeviceLimit :execrows
UPDATE account_restrictions
SET device_limit = NULL
WHERE user_id = $1 AND device_limit IS NOT NULL;

-- name: GetEnforcedDeviceLimit :one
-- 绑定事务里读取作用于该账号的上限：bot 不受限，所以 bot 与没设上限的账号都没有行。
SELECT r.device_limit
FROM account_restrictions r
JOIN users u ON u.id = r.user_id
WHERE r.user_id = $1 AND r.device_limit IS NOT NULL AND NOT u.is_bot;

-- name: CountOtherCompletedAuthorizations :one
-- 占用名额的授权：已完成登录（不含 password_pending），且不是这次要绑定的 auth key 自身。
SELECT count(*)
FROM authorizations
WHERE user_id = $1 AND auth_key_id <> $2 AND NOT password_pending;

-- name: LockUserRowForPasswordPromotion :exec
-- 2FA 转正（#106）与绑定使用同一锁顺序：先 advisory 用户锁，再锁用户行。
SELECT id FROM users WHERE id = $1 FOR UPDATE;

-- name: PromotePendingAuthorization :execrows
-- 只转正仍属于 expected user 且仍处于 password_pending 的授权。
UPDATE authorizations
SET password_pending = false, created_at = now(), active_at = now()
WHERE auth_key_id = $1 AND user_id = $2 AND password_pending;

-- name: DeletePendingAuthorization :execrows
-- 转正超限时删除这条待完成的授权；只删仍属于该用户且仍待完成的那一行。
DELETE FROM authorizations
WHERE auth_key_id = $1 AND user_id = $2 AND password_pending;
