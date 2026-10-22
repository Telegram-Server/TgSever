ALTER TABLE private_messages ADD COLUMN guestchat_via_from bigint NOT NULL DEFAULT 0 CHECK (guestchat_via_from >= 0);
ALTER TABLE message_boxes ADD COLUMN guestchat_via_from bigint NOT NULL DEFAULT 0 CHECK (guestchat_via_from >= 0);
ALTER TABLE channel_messages ADD COLUMN guestchat_via_from bigint NOT NULL DEFAULT 0 CHECK (guestchat_via_from >= 0);
ALTER TABLE private_messages ADD COLUMN business_connection_id text NOT NULL DEFAULT '', ADD COLUMN business_bot_id bigint NOT NULL DEFAULT 0 CHECK (business_bot_id >= 0);
ALTER TABLE message_boxes ADD COLUMN business_connection_id text NOT NULL DEFAULT '', ADD COLUMN business_bot_id bigint NOT NULL DEFAULT 0 CHECK (business_bot_id >= 0);
