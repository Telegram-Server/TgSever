-- 账号设备上限（#104/#105）：管理员按账号设置的最多可登录设备数，与冻结等管理员设置的限制
-- 放在同一行。NULL = 不限制。执行点在授权绑定事务里（锁住用户行后计数再插入），见
-- internal/store/postgres/authorization.go 的 bindAuthorization。
ALTER TABLE account_restrictions
    ADD COLUMN device_limit INTEGER,
    ADD CONSTRAINT account_restrictions_device_limit_check
        CHECK (device_limit IS NULL OR (device_limit BETWEEN 1 AND 100));
