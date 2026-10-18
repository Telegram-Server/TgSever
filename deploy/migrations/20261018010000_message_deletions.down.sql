-- 删除记录永久保留，回滚会丢失审计数据；此迁移只允许前滚。
DO $$ BEGIN RAISE EXCEPTION '20261018010000 preserves message deletion records; roll forward instead'; END $$;
