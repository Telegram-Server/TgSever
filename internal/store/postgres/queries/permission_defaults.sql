-- 默认权限模板（#161）：account_permission_defaults 唯一的一行 + account_permission_defaults_whitelist 名单。

-- name: GetPermissionDefaults :one
-- 模板行与名单在一条语句里读出，同一个快照：不会和一次并发的整体替换交错成"新的模板行 + 旧的名单"。
SELECT d.enabled,
       d.allow_send, d.allow_delete, d.allow_edit, d.allow_clear_history,
       d.allow_create_group, d.allow_create_channel,
       d.allow_join_group, d.allow_join_channel,
       d.allow_leave_group, d.allow_leave_channel,
       d.allow_add_contact,
       d.send_whitelist_enabled, d.apply_to_self_signup, d.updated_at, d.command_id,
       ARRAY(
           SELECT w.member_user_id FROM account_permission_defaults_whitelist w ORDER BY w.member_user_id
       )::bigint[] AS send_whitelist
FROM account_permission_defaults d;

-- name: LockPermissionDefaultsTable :exec
-- 合并更新先锁住整张模板表：还没有模板行时 FOR UPDATE 锁不住任何东西，两次首次设置要靠表锁排队。
-- SHARE ROW EXCLUSIVE 与自身互斥，不挡普通读取。
LOCK TABLE account_permission_defaults IN SHARE ROW EXCLUSIVE MODE;

-- name: UpsertPermissionDefaults :one
INSERT INTO account_permission_defaults (
    singleton, enabled,
    allow_send, allow_delete, allow_edit, allow_clear_history,
    allow_create_group, allow_create_channel,
    allow_join_group, allow_join_channel,
    allow_leave_group, allow_leave_channel,
    allow_add_contact,
    send_whitelist_enabled, apply_to_self_signup, updated_at, command_id
) VALUES (
    true, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, now(), $15
)
ON CONFLICT (singleton) DO UPDATE SET
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
    apply_to_self_signup = EXCLUDED.apply_to_self_signup,
    updated_at = EXCLUDED.updated_at,
    command_id = EXCLUDED.command_id
RETURNING updated_at;

-- name: DeletePermissionDefaultsWhitelist :exec
DELETE FROM account_permission_defaults_whitelist;

-- name: InsertPermissionDefaultsWhitelist :exec
INSERT INTO account_permission_defaults_whitelist (member_user_id)
SELECT m.member_user_id
FROM unnest(@member_user_ids::bigint[]) AS m(member_user_id)
ON CONFLICT DO NOTHING;

-- name: DeletePermissionDefaultsWhitelistMember :exec
-- 账号注销：从模板白名单中删除它。
DELETE FROM account_permission_defaults_whitelist WHERE member_user_id = @member_user_id::bigint;
