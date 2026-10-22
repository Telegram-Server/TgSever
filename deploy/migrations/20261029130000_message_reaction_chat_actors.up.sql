-- The existing indexed actor key uses positive users and negative channels.
-- Generated foreign keys keep both actor namespaces referentially intact,
-- including the same cascade semantics as the previous user-only key.
ALTER TABLE channel_message_reactions DROP CONSTRAINT channel_message_reactions_reacted_user_id_fkey;
ALTER TABLE channel_message_reactions
 ADD CONSTRAINT channel_message_reactions_actor_key_check CHECK (reacted_user_id <> 0 AND reacted_user_id <> '-9223372036854775808'::bigint),
 ADD COLUMN actor_user_id bigint GENERATED ALWAYS AS (CASE WHEN reacted_user_id > 0 THEN reacted_user_id END) STORED REFERENCES users(id) ON DELETE CASCADE,
 ADD COLUMN actor_channel_id bigint GENERATED ALWAYS AS (CASE WHEN reacted_user_id < 0 THEN -reacted_user_id END) STORED REFERENCES channels(id) ON DELETE CASCADE;
