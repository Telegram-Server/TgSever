DELETE FROM public.bot_api_updates WHERE object_payload IS NOT NULL;
DELETE FROM public.bot_api_updates WHERE reaction_payload IS NOT NULL AND coalesce((reaction_payload->>'ActorChatID')::bigint,0)>0;
ALTER TABLE public.bot_api_updates DROP COLUMN game_short_name;
DROP INDEX public.bot_api_updates_message_source_unique;
ALTER TABLE public.bot_api_updates DROP CONSTRAINT bot_api_updates_object_shape_check;
DO $$
DECLARE constraint_name text; definition text;
BEGIN
 FOREACH constraint_name IN ARRAY ARRAY['bot_api_updates_kind_check','bot_api_updates_peer_type_check','bot_api_updates_peer_id_check','bot_api_updates_message_id_check','bot_api_updates_callback_shape_check','bot_api_updates_reaction_shape_check'] LOOP
  SELECT obj_description(oid,'pg_constraint') INTO STRICT definition FROM pg_constraint WHERE conrelid='public.bot_api_updates'::regclass AND conname=constraint_name;
  IF definition IS NULL THEN RAISE EXCEPTION 'missing original definition for %',constraint_name; END IF;
  EXECUTE format('ALTER TABLE public.bot_api_updates DROP CONSTRAINT %I',constraint_name);
  EXECUTE format('ALTER TABLE public.bot_api_updates ADD CONSTRAINT %I %s',constraint_name,definition);
 END LOOP;
END $$;
ALTER TABLE public.bot_api_updates DROP COLUMN object_payload;
CREATE UNIQUE INDEX bot_api_updates_message_source_unique
 ON public.bot_api_updates (bot_user_id,update_kind,peer_type,peer_id,message_id,source_pts)
 WHERE update_kind IN ('message','edited_message') AND ephemeral_payload IS NULL;
