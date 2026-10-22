ALTER TABLE message_boxes DROP COLUMN business_bot_id, DROP COLUMN business_connection_id;
ALTER TABLE private_messages DROP COLUMN business_bot_id, DROP COLUMN business_connection_id;
ALTER TABLE channel_messages DROP COLUMN guestchat_via_from;
ALTER TABLE message_boxes DROP COLUMN guestchat_via_from;
ALTER TABLE private_messages DROP COLUMN guestchat_via_from;
