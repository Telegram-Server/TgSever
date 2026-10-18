-- my_chat_member / chat_member 没有消息可回读：peer 指向群/频道，message_id 为 0，
-- 官方 ChatMemberUpdated 的内容（操作者、新旧成员快照）整体存进 member_payload。
-- 成员变化不去重：同一个人被反复提拔/撤销，每次都是独立的一条，所以不建唯一索引。
ALTER TABLE public.bot_api_updates
    ADD COLUMN member_payload jsonb;

ALTER TABLE public.bot_api_updates
    DROP CONSTRAINT bot_api_updates_kind_check,
    DROP CONSTRAINT bot_api_updates_message_id_check,
    DROP CONSTRAINT bot_api_updates_callback_shape_check;

ALTER TABLE public.bot_api_updates
    ADD CONSTRAINT bot_api_updates_kind_check CHECK (
        update_kind IN ('message', 'edited_message', 'callback_query', 'pre_checkout_query',
                        'my_chat_member', 'chat_member')
    ),
    ADD CONSTRAINT bot_api_updates_message_id_check CHECK (
        message_id > 0
        OR (update_kind IN ('callback_query', 'pre_checkout_query', 'my_chat_member', 'chat_member')
            AND message_id = 0)
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
    ),
    ADD CONSTRAINT bot_api_updates_member_shape_check CHECK (
        member_payload IS NULL
        OR (
            update_kind IN ('my_chat_member', 'chat_member')
            AND jsonb_typeof(member_payload) = 'object'
            AND (member_payload #>> '{chat,Type}') = peer_type
            AND (member_payload #>> '{chat,ID}')::bigint = peer_id
            AND (member_payload #>> '{new,user_id}') IS NOT NULL
            AND (
                (update_kind = 'my_chat_member'
                    AND (member_payload #>> '{new,user_id}')::bigint = bot_user_id)
                OR
                (update_kind = 'chat_member'
                    AND (member_payload #>> '{new,user_id}')::bigint <> bot_user_id)
            )
        )
    );
