-- 设备绑定第 2 步（#112）：设了设备上限的账号，一台设备（auth key 上持久化的 device_id）
-- 第一次在该账号上完成登录即被绑定，名额 = 本表里该账号的行数。退出登录、撤销授权、调整
-- 或清除上限都不改这张表；账号注销（软删除，users 行保留）时在同一事务里显式删除，外键的
-- 级联删除兜住 bot 硬删等真正删除 users 行的路径。
-- device_model / platform / app_version 取自绑定那一刻的登录，仅供后台展示。
CREATE TABLE account_device_bindings (
    user_id      BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id    TEXT        NOT NULL,
    device_model TEXT        NOT NULL DEFAULT '',
    platform     TEXT        NOT NULL DEFAULT '',
    app_version  TEXT        NOT NULL DEFAULT '',
    bound_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, device_id),
    CONSTRAINT account_device_bindings_device_id_check
        CHECK (octet_length(device_id) BETWEEN 1 AND 128)
);
