-- 自动广播计划：到点由广播 worker 按计划创建一条普通的全员广播（broadcasts），
-- 后续枚举与投递完全沿用已有的广播流程。
--
-- 触发规则是结构化的“星期 + 时刻 + 时区”，不是 cron 表达式：
--   weekdays     按 Go time.Weekday 编号的位掩码，bit0 = 周日 … bit6 = 周六；127 = 每天。
--   time_of_day  当地时刻，按一天中的分钟数存（14:00 = 840）。
--   timezone     IANA 时区名，下次触发时间按它换算，夏令时由 Go 的时区库处理。
-- next_run_at 由应用计算；停用的计划为 NULL。worker 在同一个事务里锁住到期计划、
-- 建出广播并推进 next_run_at，多实例或重启都不会重复发送。
--
-- 上一轮同一计划的广播还没投递完、错过触发超过宽限期、或者没有可发的账号时，
-- 本次跳过，记在 last_skipped_at / last_skip_reason / skipped_count 上。
CREATE TABLE broadcast_schedules (
    id bigserial PRIMARY KEY,
    message text NOT NULL CHECK (message <> '' AND octet_length(message) <= 4096),
    timezone varchar(64) NOT NULL CHECK (timezone <> ''),
    time_of_day integer NOT NULL CHECK (time_of_day >= 0 AND time_of_day < 1440),
    weekdays smallint NOT NULL CHECK (weekdays > 0 AND weekdays <= 127),
    enabled boolean NOT NULL,
    next_run_at timestamptz,
    last_run_at timestamptz,
    last_broadcast_id bigint,
    run_count bigint NOT NULL DEFAULT 0 CHECK (run_count >= 0),
    last_skipped_at timestamptz,
    last_skip_reason varchar(32) NOT NULL DEFAULT '',
    skipped_count bigint NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
    created_by varchar(128) NOT NULL DEFAULT '',
    updated_by varchar(128) NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((enabled AND next_run_at IS NOT NULL) OR (NOT enabled AND next_run_at IS NULL))
);

CREATE INDEX broadcast_schedules_due_idx ON broadcast_schedules (next_run_at, id)
    WHERE enabled;

-- 由计划生成的广播记下来源；删除计划不删历史广播，只断开关联。
ALTER TABLE broadcasts
    ADD COLUMN schedule_id bigint REFERENCES broadcast_schedules(id) ON DELETE SET NULL;

CREATE INDEX broadcasts_schedule_idx ON broadcasts (schedule_id, id DESC)
    WHERE schedule_id IS NOT NULL;
