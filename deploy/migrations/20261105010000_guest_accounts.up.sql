-- 游客注册（telesrv.signUpAsGuest，docs/guest-login.md）。
--
-- guest_accounts 标记经游客注册创建的账号（后台按它筛选、统计）。只记来源，不随账号
-- 后来设置密码或换绑号码而清除。
CREATE TABLE guest_accounts (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    -- 注册时分配的号码；与 users.phone 不同说明账号已换绑号码。
    phone TEXT NOT NULL,
    -- 注册请求的 IP 键（IPv4 原样，IPv6 取 /64 前缀）。
    ip_key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX guest_accounts_created_at_idx ON guest_accounts (created_at DESC);

-- guest_ip_bindings 是「同一 IP 在 X 天内再次游客注册得到同一个账号」的绑定，每个 IP 键
-- 一行。绑定过期或账号已转正（设置了登录密码 / 2FA、换绑号码、注销）时由下一次游客注册
-- 换成新账号；换的时候按 user_id 比较并交换，同一 IP 并发注册只有一个能建号。
CREATE TABLE guest_ip_bindings (
    ip_key TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    bound_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX guest_ip_bindings_user_id_idx ON guest_ip_bindings (user_id);
