-- 手机号 + 密码登录（telesrv 扩展协议，#27）：账号的登录密码，与 2FA 云密码（account_passwords）
-- 互相独立。服务端只保存 SRP verifier：salt1 = 服务端基础盐 + 客户端追加的 32 字节，
-- salt2 = new_algo.salt2，verifier = 定长 256 字节的 v。
--
-- 用户是软删除，外键级联只覆盖硬删除（测试清理、运维脚本）；注销账号时要由业务层显式删除
-- 登录密码（由 #34 接入账号注销流程）。
CREATE TABLE login_passwords (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    salt1 BYTEA NOT NULL,
    salt2 BYTEA NOT NULL,
    verifier BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT login_passwords_material_check
        CHECK (octet_length(salt1) > 0 AND octet_length(salt2) > 0 AND octet_length(verifier) > 0)
);
