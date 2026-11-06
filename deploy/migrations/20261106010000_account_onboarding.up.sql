-- 新账号默认入驻：入驻模板（默认联系人 + 自动加入的群和频道）与入驻任务。
--
-- 模板：account_onboarding_defaults 唯一的一行记录最近一次写入（没有这一行 = 没有设置过），两张
-- 名单表按 position 保存配置顺序。每次设置在同一事务里整体替换。名单指向的账号或频道真正删除行时
-- 外键级联删除条目；软删除（注销）的账号由执行任务时跳过。
CREATE TABLE account_onboarding_defaults (
    singleton  BOOLEAN     PRIMARY KEY DEFAULT true CHECK (singleton),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    command_id TEXT        NOT NULL DEFAULT ''
);

CREATE TABLE account_onboarding_default_contacts (
    contact_user_id BIGINT  PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    position        INTEGER NOT NULL,
    mutual          BOOLEAN NOT NULL DEFAULT false
);

CREATE TABLE account_onboarding_default_channels (
    channel_id BIGINT  PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL
);

-- 任务：建号事务里按当时的模板写入（快照），执行完删除。available_at 同时充当租约与重试时间：
-- 领取时推到 now() + 租约，失败重试时推到 now() + 退避；status = 'abandoned' 的任务不再被领取。
CREATE TABLE account_onboarding_jobs (
    user_id          BIGINT      PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    contact_user_ids BIGINT[]    NOT NULL,
    contact_mutual   BOOLEAN[]   NOT NULL,
    channel_ids      BIGINT[]    NOT NULL,
    status           TEXT        NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'abandoned')),
    attempts         INTEGER     NOT NULL DEFAULT 0,
    last_error       TEXT        NOT NULL DEFAULT '',
    available_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX account_onboarding_jobs_claimable
    ON account_onboarding_jobs (available_at, created_at)
    WHERE status = 'pending';
