-- name: UpsertPushDevice :exec
INSERT INTO push_devices (auth_key_id, token_type, token, user_id, app_sandbox, secret, no_muted, other_uids, lang_code)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
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
SELECT auth_key_id, token_type, token, user_id, app_sandbox, secret, no_muted, other_uids, lang_code, created_at
FROM push_devices
WHERE user_id = $1;

-- name: DeletePushDevice :execrows
DELETE FROM push_devices WHERE auth_key_id = $1 AND token_type = $2;

-- name: DeleteStalePushDevice :execrows
DELETE FROM push_devices
WHERE auth_key_id = $1 AND token_type = $2 AND token = $3 AND user_id = $4;

-- name: ListPushDevicesByUsers :many
SELECT auth_key_id, token_type, token, user_id, app_sandbox, secret, no_muted, other_uids, lang_code, created_at
FROM push_devices
WHERE user_id = ANY(@user_ids::bigint[]);
