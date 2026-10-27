ALTER TABLE public.scheduled_messages
    DROP COLUMN IF EXISTS forwarded_post_message_id,
    DROP COLUMN IF EXISTS forwarded_post_channel_id;
