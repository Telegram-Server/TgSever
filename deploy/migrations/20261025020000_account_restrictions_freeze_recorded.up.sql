-- 设备上限（#104）与冻结共用 account_restrictions 一行，但只为上限建出来的行不是冻结记录。
-- freeze_recorded 明确标出"这一行写过冻结状态"：冻结读路径只认 freeze_recorded 的行，
-- 只为上限插入的行写 false。迁移前的所有行（含 0032 起 command_id 为空的旧发送限制行）
-- 都是冻结写入留下的，默认值 true 原样保留它们的语义。
ALTER TABLE account_restrictions
    ADD COLUMN freeze_recorded boolean NOT NULL DEFAULT true,
    ADD CONSTRAINT account_restrictions_freeze_recorded_check
        CHECK (freeze_recorded OR NOT frozen);
