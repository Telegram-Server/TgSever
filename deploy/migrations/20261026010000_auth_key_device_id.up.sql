-- 设备绑定第 1 步（#111）：客户端在 initConnection.params 里上报的稳定设备标识。
-- NULL = 从未上报。随"按 auth key 持久化客户端信息"链路写入，一次写入（只在 NULL 时写）；PFS 下
-- 由 temp key 绑定到 perm key 时一并带到 perm key 上。合法值由应用层校验（1–128 字节
-- 可打印 ASCII），这里只兜住长度。
ALTER TABLE auth_keys
    ADD COLUMN device_id TEXT,
    ADD CONSTRAINT auth_keys_device_id_check
        CHECK (device_id IS NULL OR octet_length(device_id) BETWEEN 1 AND 128);
