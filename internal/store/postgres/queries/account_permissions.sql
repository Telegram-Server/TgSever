-- 员工权限控制（#119）：account_permissions 一行 + account_send_whitelist 名单。

-- name: GetAccountPermissions :one
-- 权限行与名单在一条语句里读出，同一个快照：不会和一次并发的整体替换交错成"新的权限行 +
-- 旧的名单"。FOR UPDATE 版本见 LockAccountPermissions。
SELECT p.user_id, p.enabled,
       p.allow_send, p.allow_delete, p.allow_edit, p.allow_clear_history,
       p.allow_create_group, p.allow_create_channel,
       p.allow_join_group, p.allow_join_channel,
       p.allow_leave_group, p.allow_leave_channel,
       p.allow_add_contact,
       p.send_whitelist_enabled, p.updated_at, p.command_id,
       ARRAY(
           SELECT w.member_user_id FROM account_send_whitelist w
           WHERE w.user_id = p.user_id ORDER BY w.member_user_id
       )::bigint[] AS send_whitelist
FROM account_permissions p
WHERE p.user_id = $1;

-- name: LockAccountPermissions :one
-- 部分更新在事务里先锁住权限行再合并，两个并发的部分更新因此排队，后提交的那次基于先提交的
-- 结果合并，不会把对方的改动覆盖回去。名单行由同一把行锁保护（写名单的事务都先锁这一行）。
SELECT p.user_id, p.enabled,
       p.allow_send, p.allow_delete, p.allow_edit, p.allow_clear_history,
       p.allow_create_group, p.allow_create_channel,
       p.allow_join_group, p.allow_join_channel,
       p.allow_leave_group, p.allow_leave_channel,
       p.allow_add_contact,
       p.send_whitelist_enabled, p.updated_at, p.command_id,
       ARRAY(
           SELECT w.member_user_id FROM account_send_whitelist w
           WHERE w.user_id = p.user_id ORDER BY w.member_user_id
       )::bigint[] AS send_whitelist
FROM account_permissions p
WHERE p.user_id = $1
FOR UPDATE OF p;

-- name: UpsertAccountPermissions :one
-- 整体替换的第一步：upsert 拿到这一行的行锁，并发的设置在这里排队，随后的
-- "删名单 + 写名单"因此不会与别的提交穿插。
INSERT INTO account_permissions (
    user_id, enabled,
    allow_send, allow_delete, allow_edit, allow_clear_history,
    allow_create_group, allow_create_channel,
    allow_join_group, allow_join_channel,
    allow_leave_group, allow_leave_channel,
    allow_add_contact,
    send_whitelist_enabled, updated_at, command_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now(), $15
)
ON CONFLICT (user_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    allow_send = EXCLUDED.allow_send,
    allow_delete = EXCLUDED.allow_delete,
    allow_edit = EXCLUDED.allow_edit,
    allow_clear_history = EXCLUDED.allow_clear_history,
    allow_create_group = EXCLUDED.allow_create_group,
    allow_create_channel = EXCLUDED.allow_create_channel,
    allow_join_group = EXCLUDED.allow_join_group,
    allow_join_channel = EXCLUDED.allow_join_channel,
    allow_leave_group = EXCLUDED.allow_leave_group,
    allow_leave_channel = EXCLUDED.allow_leave_channel,
    allow_add_contact = EXCLUDED.allow_add_contact,
    send_whitelist_enabled = EXCLUDED.send_whitelist_enabled,
    updated_at = EXCLUDED.updated_at,
    command_id = EXCLUDED.command_id
RETURNING updated_at;

-- name: DeleteAccountSendWhitelist :exec
DELETE FROM account_send_whitelist WHERE user_id = $1;

-- name: InsertAccountSendWhitelist :exec
INSERT INTO account_send_whitelist (user_id, member_user_id)
SELECT @user_id::bigint, m.member_user_id
FROM unnest(@member_user_ids::bigint[]) AS m(member_user_id)
ON CONFLICT DO NOTHING;

-- name: BumpAccountPermissionsReadModel :exec
-- 提交后经 telesrv_read_model_changed 通知所有实例失效该账号的权限视图缓存。
SELECT telesrv_bump_read_model_version('account_permissions', 0, 'user', @user_id::bigint);

-- name: DeleteAccountPermissionsByUser :exec
-- 账号注销：删除它自己的权限设置（名单随外键级联），并通知失效。
WITH deleted AS (
    DELETE FROM account_permissions WHERE user_id = @user_id::bigint RETURNING user_id
)
SELECT telesrv_bump_read_model_version('account_permissions', 0, 'user', deleted.user_id)
FROM deleted;

-- name: DeleteAccountSendWhitelistByMember :exec
-- 账号注销：删除别的账号白名单里指向它的条目，并通知失效这些账号的权限视图。
WITH deleted AS (
    DELETE FROM account_send_whitelist WHERE member_user_id = @member_user_id::bigint RETURNING user_id
)
SELECT telesrv_bump_read_model_version('account_permissions', 0, 'user', owners.user_id)
FROM (SELECT DISTINCT user_id FROM deleted) AS owners;
