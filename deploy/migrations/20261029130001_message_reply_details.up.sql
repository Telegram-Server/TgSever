ALTER TABLE private_messages DROP CONSTRAINT private_messages_reply_external_object;
ALTER TABLE message_boxes DROP CONSTRAINT message_boxes_reply_external_object;
ALTER TABLE private_messages ADD CONSTRAINT private_messages_reply_external_object CHECK(jsonb_typeof(reply_external)='object' AND octet_length(reply_external::text)<=1052672);
ALTER TABLE message_boxes ADD CONSTRAINT message_boxes_reply_external_object CHECK(jsonb_typeof(reply_external)='object' AND octet_length(reply_external::text)<=1052672);
-- Upgrade the existing private reply JSON cell to a strict versioned detail
-- envelope. The source snapshot remains immutable; local task/option headers
-- now use the same durable cell without adding duplicate message fact columns.
UPDATE private_messages SET reply_external=jsonb_build_object('version',1,'external',reply_external) WHERE reply_external<>'{}'::jsonb;
UPDATE message_boxes SET reply_external=jsonb_build_object('version',1,'external',reply_external) WHERE reply_external<>'{}'::jsonb;
