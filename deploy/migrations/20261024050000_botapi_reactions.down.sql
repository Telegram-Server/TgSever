DELETE FROM public.bot_api_updates WHERE update_kind IN ('message_reaction', 'message_reaction_count');
ALTER TABLE public.bot_api_updates
    DROP CONSTRAINT bot_api_updates_reaction_shape_check,
    DROP CONSTRAINT bot_api_updates_kind_check,
    DROP CONSTRAINT bot_api_updates_callback_shape_check;
ALTER TABLE public.bot_api_updates
    ADD CONSTRAINT bot_api_updates_kind_check CHECK (
        update_kind IN ('message', 'edited_message', 'callback_query', 'pre_checkout_query', 'my_chat_member', 'chat_member')
    ),
    ADD CONSTRAINT bot_api_updates_callback_shape_check CHECK (
        (update_kind = 'callback_query'
            AND callback_query_id <> 0
            AND callback_user_id > 0
            AND callback_chat_instance <> 0
            AND COALESCE(octet_length(callback_data), 0) <= 64
            AND source_pts = 0
            AND (
                (peer_type IN ('user', 'channel') AND peer_id > 0 AND message_id > 0
                    AND callback_inline_dc_id = 0 AND callback_inline_owner_id = 0
                    AND callback_inline_message_id = 0 AND callback_inline_access_hash = 0)
                OR
                (peer_type = '' AND peer_id = 0 AND message_id = 0
                    AND callback_inline_dc_id > 0 AND callback_inline_owner_id > 0
                    AND callback_inline_message_id > 0 AND callback_inline_access_hash <> 0)
            ))
        OR
        (update_kind = 'pre_checkout_query'
            AND pre_checkout_payload IS NOT NULL
            AND callback_query_id = 0 AND callback_user_id = 0
            AND callback_chat_instance = 0 AND callback_data IS NULL
            AND callback_inline_dc_id = 0 AND callback_inline_owner_id = 0
            AND callback_inline_message_id = 0 AND callback_inline_access_hash = 0
            AND source_pts = 0
            AND peer_type = '' AND peer_id = 0 AND message_id = 0)
        OR
        (update_kind IN ('message', 'edited_message')
            AND peer_type IN ('user', 'channel') AND peer_id > 0 AND message_id > 0
            AND callback_query_id = 0 AND callback_user_id = 0
            AND callback_chat_instance = 0 AND callback_data IS NULL
            AND callback_inline_dc_id = 0 AND callback_inline_owner_id = 0
            AND callback_inline_message_id = 0 AND callback_inline_access_hash = 0)
        OR
        (update_kind IN ('my_chat_member', 'chat_member')
            AND peer_type = 'channel' AND peer_id > 0 AND message_id = 0 AND source_pts = 0
            AND callback_query_id = 0 AND callback_user_id = 0
            AND callback_chat_instance = 0 AND callback_data IS NULL
            AND callback_inline_dc_id = 0 AND callback_inline_owner_id = 0
            AND callback_inline_message_id = 0 AND callback_inline_access_hash = 0
            AND ephemeral_payload IS NULL AND pre_checkout_payload IS NULL
            AND member_payload IS NOT NULL)
    );
ALTER TABLE public.bot_api_updates DROP COLUMN reaction_payload;
