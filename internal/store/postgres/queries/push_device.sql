-- name: UpsertPushDevice :exec
-- push_enabled 只在插入新登记时写入；重新登记（app 重启、token 轮换）保留后台设置的开关，
-- 所以不在 DO UPDATE 的更新集合里。
INSERT INTO push_devices (auth_key_id, token_type, token, user_id, app_sandbox, secret, no_muted, other_uids, lang_code, push_enabled)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (auth_key_id, token_type) DO UPDATE
SET token = EXCLUDED.token,
    user_id = EXCLUDED.user_id,
    app_sandbox = EXCLUDED.app_sandbox,
    secret = EXCLUDED.secret,
    no_muted = EXCLUDED.no_muted,
    other_uids = EXCLUDED.other_uids,
    lang_code = EXCLUDED.lang_code,
    created_at = now();

-- name: ListPushDevicesByUser :many
-- 连同授权当前的 user 一起读出（推送发送前核对，避免逐台查询授权）。
SELECT sqlc.embed(push_devices), authorizations.user_id AS authorization_user_id
FROM push_devices
JOIN authorizations ON authorizations.auth_key_id = push_devices.auth_key_id
WHERE push_devices.user_id = $1;

-- name: DeletePushDevice :execrows
DELETE FROM push_devices WHERE auth_key_id = $1 AND token_type = $2 AND token = $3;

-- name: DeleteStalePushDevice :execrows
DELETE FROM push_devices
WHERE auth_key_id = $1 AND token_type = $2 AND token = $3 AND user_id = $4;

-- name: ListPushDevicesByUsers :many
SELECT sqlc.embed(push_devices), authorizations.user_id AS authorization_user_id
FROM push_devices
JOIN authorizations ON authorizations.auth_key_id = push_devices.auth_key_id
WHERE push_devices.user_id = ANY(@user_ids::bigint[]);

-- name: SetPushDevicesEnabled :execrows
-- 后台推送开关：一个授权下该用户的全部登记（APNs 与 VoIP）一起开关。
UPDATE push_devices SET push_enabled = @push_enabled
WHERE auth_key_id = @auth_key_id AND user_id = @user_id;

-- name: AdminPushDeviceStatesByUser :many
-- 后台账号详情的推送开关：用户每个授权下的 APNs/VoIP 登记是否全部开启（有一条关闭即算关闭）。
-- 只算登记时与授权当前都属于该用户的登记，与推送服务的发送判定一致。
SELECT push_devices.auth_key_id, bool_and(push_devices.push_enabled)::boolean AS push_enabled
FROM push_devices
JOIN authorizations ON authorizations.auth_key_id = push_devices.auth_key_id
WHERE push_devices.user_id = @user_id
  AND authorizations.user_id = @user_id
  AND push_devices.token_type = ANY(@token_types::integer[])
GROUP BY push_devices.auth_key_id;
