-- 按标题查频道（频道导入的 /v1/channel-imports/channels?q=）：已有的 channels_public_title_trgm_idx
-- 只覆盖有公开用户名的频道，私有频道的标题匹配会全表扫描。这个索引覆盖全部频道，含已删除的
-- （查询可以要求包含已删除的频道，带 WHERE NOT deleted 的部分索引那时用不上）。
CREATE INDEX channels_title_trgm_idx ON channels USING gin (lower(title) gin_trgm_ops);
