-- 默认权限模板（#156/#161）：唯一的一行，列与 account_permissions 一致（没有 user_id），加上
-- "自助注册也套用"开关。没有这一行 = 没有模板。模板是快照：建号时复制给新账号，修改模板不影响
-- 已有账号。主键恒为 true，表里最多一行。
CREATE TABLE account_permission_defaults (
    singleton              BOOLEAN     PRIMARY KEY DEFAULT true CHECK (singleton),
    enabled                BOOLEAN     NOT NULL DEFAULT false,
    allow_send             BOOLEAN     NOT NULL DEFAULT true,
    allow_delete           BOOLEAN     NOT NULL DEFAULT true,
    allow_edit             BOOLEAN     NOT NULL DEFAULT true,
    allow_clear_history    BOOLEAN     NOT NULL DEFAULT true,
    allow_create_group     BOOLEAN     NOT NULL DEFAULT true,
    allow_create_channel   BOOLEAN     NOT NULL DEFAULT true,
    allow_join_group       BOOLEAN     NOT NULL DEFAULT true,
    allow_join_channel     BOOLEAN     NOT NULL DEFAULT true,
    allow_leave_group      BOOLEAN     NOT NULL DEFAULT true,
    allow_leave_channel    BOOLEAN     NOT NULL DEFAULT true,
    allow_add_contact      BOOLEAN     NOT NULL DEFAULT true,
    send_whitelist_enabled BOOLEAN     NOT NULL DEFAULT false,
    apply_to_self_signup   BOOLEAN     NOT NULL DEFAULT false,
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    command_id             TEXT        NOT NULL DEFAULT ''
);

-- 模板的发送白名单。每次设置在同一事务里整体替换；账号注销（软删除）时在注销事务里显式删除
-- 指向它的条目，外键级联兜住真正删除 users 行的路径。
CREATE TABLE account_permission_defaults_whitelist (
    member_user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE
);
