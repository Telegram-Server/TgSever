DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM message_boxes WHERE reply_external ? 'checklist_task_id' OR reply_external ? 'poll_option') OR EXISTS(SELECT 1 FROM private_messages WHERE reply_external ? 'checklist_task_id' OR reply_external ? 'poll_option') THEN
  RAISE EXCEPTION 'cannot discard durable checklist or poll reply headers';
 END IF;
END $$;
UPDATE private_messages SET reply_external=COALESCE(reply_external->'external','{}'::jsonb) WHERE reply_external<>'{}'::jsonb;
UPDATE message_boxes SET reply_external=COALESCE(reply_external->'external','{}'::jsonb) WHERE reply_external<>'{}'::jsonb;
ALTER TABLE private_messages DROP CONSTRAINT private_messages_reply_external_object;
ALTER TABLE message_boxes DROP CONSTRAINT message_boxes_reply_external_object;
ALTER TABLE private_messages ADD CONSTRAINT private_messages_reply_external_object CHECK(jsonb_typeof(reply_external)='object' AND octet_length(reply_external::text)<=1048576);
ALTER TABLE message_boxes ADD CONSTRAINT message_boxes_reply_external_object CHECK(jsonb_typeof(reply_external)='object' AND octet_length(reply_external::text)<=1048576);
