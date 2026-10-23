-- bot qts 序列（#145）：每个 bot 账号一条连续的 qts 序列。
--
-- bot 不能用密聊，所以 bot 账号 updates.getState / getDifference 里的 qts 完全来自这里；
-- 普通用户的密聊 qts（按设备计数）不受影响。源事件（成员变动等）与记录在同一事务里写入，
-- qts 由 bot_update_states 的行锁原子分配。
--
-- bot_update_states.qts 是已分配的最大 qts。保留期回收只删 bot_updates，不回退 qts：
-- 客户端请求的 qts 早于最小保留记录时从最小保留记录补起，被回收的更新视为丢弃（不用
-- updates.differenceTooLong：TDLib 收到它只重置 pts、带同一个旧 qts 再请求，会循环）。
CREATE TABLE bot_update_states (
    bot_user_id bigint PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    qts integer NOT NULL DEFAULT 0 CHECK (qts >= 0)
);

-- payload 是领域层的更新内容（JSON），按接收者投影在读出时进行。
CREATE TABLE bot_updates (
    bot_user_id bigint NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    qts integer NOT NULL CHECK (qts > 0),
    date integer NOT NULL,
    payload jsonb NOT NULL,
    PRIMARY KEY (bot_user_id, qts)
);

-- maintenance 按 date 回收超过保留期的记录。
CREATE INDEX bot_updates_date_idx ON bot_updates (date);
