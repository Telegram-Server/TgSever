-- 员工权限控制（#118/#119）：按账号的总开关、11 项"是否允许"的权限（true = 允许）、发送白名单
-- 开关，以及最近一次写入的时间与后台命令 id。没有行 = 不受控制；关闭总开关保留各项设置。
-- 不复用 account_restrictions：冻结与设备上限已共用那一行，再叠一组语义会更难区分。
-- 账号注销（软删除，users 行保留）时在同一事务里显式删除，外键级联兜住真正删除 users 行的路径。
CREATE TABLE account_permissions (
    user_id                BIGINT      PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
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
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    command_id             TEXT        NOT NULL DEFAULT ''
);

-- 发送白名单：总开关与白名单开关都开启时，私聊（含秘密聊天）只能发给名单里的人。每次设置
-- 在同一事务里整体替换该账号的全部行。
CREATE TABLE account_send_whitelist (
    user_id        BIGINT NOT NULL REFERENCES account_permissions(user_id) ON DELETE CASCADE,
    member_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, member_user_id)
);

-- 账号注销时删除别的账号白名单里指向它的条目。
CREATE INDEX account_send_whitelist_member_idx ON account_send_whitelist (member_user_id);
