-- 通知声音按平台分槽：{"ios":{...},"android":{...},"other":{...}}，每槽是
-- {"kind":"default|none|local|ringtone","title","data","id"}。NULL=三槽都未设置。
ALTER TABLE public.notify_settings
    ADD COLUMN sound jsonb,
    ADD COLUMN stories_sound jsonb;
