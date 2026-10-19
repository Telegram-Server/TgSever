ALTER TABLE public.scheduled_messages
    DROP CONSTRAINT IF EXISTS scheduled_messages_fwd_saved_from_peer_type_check,
    DROP COLUMN IF EXISTS fwd_saved_from_msg_id,
    DROP COLUMN IF EXISTS fwd_saved_from_peer_id,
    DROP COLUMN IF EXISTS fwd_saved_from_peer_type,
    DROP COLUMN IF EXISTS fwd_channel_post;
