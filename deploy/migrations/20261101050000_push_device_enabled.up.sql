-- 后台推送开关（push.manage）：为 false 时推送服务不向这条登记发送任何推送。
-- 已有登记全部保持开启；新登记的值由应用按 TELESRV_PUSH_NEW_DEVICE_ENABLED 显式写入，
-- 所以去掉默认值，漏写的插入直接报错而不是悄悄开启。重新登记（ON CONFLICT）不改它。
ALTER TABLE push_devices ADD COLUMN push_enabled BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE push_devices ALTER COLUMN push_enabled DROP DEFAULT;
