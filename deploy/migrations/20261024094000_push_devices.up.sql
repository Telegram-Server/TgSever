-- iOS 离线推送（#93/#94）：account.registerDevice 登记的设备推送 token。
-- 唯一性按 (授权, token 类型)，同一授权重复登记覆盖旧值；同一个 token 可以挂在多个授权下
-- （同一台 iPhone 登录多个账号）。外键级联到 authorizations：退出登录、终止会话、注销账号、
-- 管理员吊销、bot 重置等删除授权的路径都会自动带走登记。
-- user_id 是登记时授权所属的用户；授权的 user 可能被原地改写（扫码登录），发送前要核对。
-- secret 为空表示客户端不要求加密；lang_code 是登记时客户端的语言，用于服务端渲染推送文案。
CREATE TABLE push_devices (
    auth_key_id BIGINT NOT NULL REFERENCES authorizations(auth_key_id) ON DELETE CASCADE,
    token_type INTEGER NOT NULL,
    token TEXT NOT NULL,
    user_id BIGINT NOT NULL,
    app_sandbox BOOLEAN NOT NULL DEFAULT false,
    secret BYTEA NOT NULL DEFAULT ''::bytea,
    no_muted BOOLEAN NOT NULL DEFAULT false,
    other_uids BIGINT[] NOT NULL DEFAULT '{}',
    lang_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (auth_key_id, token_type)
);

CREATE INDEX push_devices_user_id_idx ON push_devices (user_id);
