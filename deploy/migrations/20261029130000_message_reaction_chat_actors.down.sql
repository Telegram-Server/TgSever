DELETE FROM channel_message_reactions WHERE reacted_user_id < 0;
ALTER TABLE channel_message_reactions
 DROP COLUMN actor_user_id,
 DROP COLUMN actor_channel_id,
 DROP CONSTRAINT channel_message_reactions_actor_key_check;
ALTER TABLE channel_message_reactions ADD CONSTRAINT channel_message_reactions_reacted_user_id_fkey FOREIGN KEY (reacted_user_id) REFERENCES users(id) ON DELETE CASCADE;
