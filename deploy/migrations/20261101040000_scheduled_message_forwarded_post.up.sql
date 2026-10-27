-- 定时转发保留源频道帖子（#166）：转发数在到点发出的发送事务里计，排程时不计、取消的定时转发
-- 不计，所以派发时要知道源帖子。两列为 0 表示不是转发频道帖子。
ALTER TABLE public.scheduled_messages
    ADD COLUMN forwarded_post_channel_id bigint DEFAULT 0 NOT NULL,
    ADD COLUMN forwarded_post_message_id integer DEFAULT 0 NOT NULL;
