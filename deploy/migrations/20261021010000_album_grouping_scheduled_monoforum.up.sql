-- 定时消息保留相册分组：定时 sendMultiMedia / 定时转发相册的同组各条共享一个非零 grouped_id，
-- 列表投影与到点发出都沿用它。单条定时消息为 0。monoforum 写的是 channel_messages，已有 grouped_id 列。
ALTER TABLE public.scheduled_messages
    ADD COLUMN grouped_id bigint DEFAULT 0 NOT NULL;
