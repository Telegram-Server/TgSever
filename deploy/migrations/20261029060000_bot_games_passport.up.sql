CREATE TABLE bot_games (
 id bigint PRIMARY KEY,
 bot_user_id bigint NOT NULL REFERENCES users(id),
 short_name text NOT NULL,
 game jsonb NOT NULL,
 UNIQUE(bot_user_id,short_name)
);
CREATE TABLE bot_game_scores (
 game_id bigint NOT NULL REFERENCES bot_games(id) ON DELETE CASCADE,
 chat_id bigint NOT NULL,
 inline_owner_id bigint NOT NULL DEFAULT 0,
 inline_message_id integer NOT NULL DEFAULT 0,
 user_id bigint NOT NULL REFERENCES users(id),
 score integer NOT NULL CHECK (score>=0),
 PRIMARY KEY(game_id,chat_id,inline_owner_id,inline_message_id,user_id)
);
CREATE TABLE passport_user_data (
 user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 data jsonb NOT NULL DEFAULT '{"elements":[],"authorizations":[]}'::jsonb
);
ALTER TABLE account_passwords ADD COLUMN secure_settings jsonb;
CREATE TABLE bot_passport_public_keys (
 bot_user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 public_key text NOT NULL
);
CREATE TABLE bot_business_game_messages (
 bot_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 chat_id bigint NOT NULL,
 message_id integer NOT NULL,
 owner_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 connection_id text NOT NULL,
 PRIMARY KEY(bot_user_id,chat_id,message_id,owner_user_id)
);
