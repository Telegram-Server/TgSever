ALTER TABLE public.bot_api_updates ADD COLUMN object_payload jsonb;
ALTER TABLE public.bot_api_updates ADD COLUMN game_short_name text NOT NULL DEFAULT '';
ALTER TABLE public.bot_api_updates ADD CONSTRAINT bot_api_updates_game_shape_check CHECK (
 game_short_name='' OR (update_kind='callback_query' AND length(game_short_name)<=64 AND coalesce(octet_length(callback_data),0)=0)
);

-- Retain the existing nine payload checks verbatim for legacy rows. Public
-- snapshots use their own mutually exclusive shape; no historical row changes.
DO $$
DECLARE constraint_name text; definition text;
BEGIN
 FOREACH constraint_name IN ARRAY ARRAY['bot_api_updates_kind_check','bot_api_updates_peer_type_check','bot_api_updates_peer_id_check','bot_api_updates_message_id_check','bot_api_updates_callback_shape_check'] LOOP
  SELECT pg_get_constraintdef(oid) INTO STRICT definition FROM pg_constraint WHERE conrelid='public.bot_api_updates'::regclass AND conname=constraint_name;
  EXECUTE format('ALTER TABLE public.bot_api_updates DROP CONSTRAINT %I',constraint_name);
  EXECUTE format('ALTER TABLE public.bot_api_updates ADD CONSTRAINT %I CHECK (object_payload IS NOT NULL OR %s)',constraint_name,substring(definition FROM 7));
  EXECUTE format('COMMENT ON CONSTRAINT %I ON public.bot_api_updates IS %L',constraint_name,definition);
 END LOOP;
END $$;

ALTER TABLE public.bot_api_updates ADD CONSTRAINT bot_api_updates_object_shape_check CHECK (
 (object_payload IS NULL AND update_kind IN ('message','edited_message','callback_query','pre_checkout_query','my_chat_member','chat_member','message_reaction','message_reaction_count','chat_join_request'))
 OR
 (object_payload IS NOT NULL AND update_kind IN ('message','edited_message','chat_join_request','channel_post','edited_channel_post','business_connection','business_message','edited_business_message','deleted_business_messages','guest_message','inline_query','chosen_inline_result','shipping_query','purchased_paid_media','poll','poll_answer','chat_boost','removed_chat_boost','managed_bot','subscription','stopped_message_generation')
 AND peer_type='' AND peer_id=0 AND message_id=0 AND source_pts=0 AND date>0
 AND callback_query_id=0 AND callback_user_id=0 AND callback_chat_instance=0 AND callback_data IS NULL
 AND callback_inline_dc_id=0 AND callback_inline_owner_id=0 AND callback_inline_message_id=0 AND callback_inline_access_hash=0
 AND ephemeral_payload IS NULL AND pre_checkout_payload IS NULL AND member_payload IS NULL AND reaction_payload IS NULL AND join_request_payload IS NULL
 AND jsonb_typeof(object_payload)='object' AND object_payload->>'kind'=update_kind AND jsonb_typeof(object_payload->'payload')='object')
);

DROP INDEX public.bot_api_updates_message_source_unique;
CREATE UNIQUE INDEX bot_api_updates_message_source_unique
 ON public.bot_api_updates (bot_user_id,update_kind,peer_type,peer_id,message_id,source_pts)
 WHERE update_kind IN ('message','edited_message') AND ephemeral_payload IS NULL AND object_payload IS NULL;

DO $$
DECLARE definition text;
BEGIN
 SELECT pg_get_constraintdef(oid) INTO STRICT definition FROM pg_constraint WHERE conrelid='public.bot_api_updates'::regclass AND conname='bot_api_updates_reaction_shape_check';
 ALTER TABLE public.bot_api_updates DROP CONSTRAINT bot_api_updates_reaction_shape_check;
 ALTER TABLE public.bot_api_updates ADD CONSTRAINT bot_api_updates_reaction_shape_check CHECK (
  (reaction_payload IS NULL AND update_kind NOT IN ('message_reaction','message_reaction_count'))
  OR (update_kind IN ('message_reaction','message_reaction_count')
   AND jsonb_typeof(reaction_payload)='object'
   AND (reaction_payload #>> '{chat,Type}')=peer_type
   AND (reaction_payload #>> '{chat,ID}')::bigint=peer_id
   AND (reaction_payload->>'MessageID')::integer=message_id
   AND ((coalesce((reaction_payload->>'ActorUserID')::bigint,0)>0 AND coalesce((reaction_payload->>'ActorChatID')::bigint,0)=0)
    OR (coalesce((reaction_payload->>'ActorChatID')::bigint,0)>0 AND coalesce((reaction_payload->>'ActorUserID')::bigint,0)=0))
   AND (reaction_payload->>'date')::integer=date
   AND (update_kind<>'message_reaction_count' OR peer_type='channel'))
 );
 EXECUTE format('COMMENT ON CONSTRAINT bot_api_updates_reaction_shape_check ON public.bot_api_updates IS %L',definition);
END $$;
