ALTER TABLE bots ADD COLUMN guest_chat BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE bots ADD COLUMN can_manage_bots BOOLEAN NOT NULL DEFAULT FALSE;
-- The built-in BotFather is the initial manager; user-created bots opt in.
UPDATE bots SET can_manage_bots=TRUE WHERE bot_user_id=93372553;
CREATE TABLE bot_management (
 bot_user_id BIGINT PRIMARY KEY REFERENCES bots(bot_user_id) ON DELETE CASCADE,
 manager_bot_id BIGINT REFERENCES bots(bot_user_id) ON DELETE SET NULL,
 is_access_restricted BOOLEAN NOT NULL DEFAULT FALSE,
 added_user_ids BIGINT[] NOT NULL DEFAULT '{}',
 CHECK (manager_bot_id IS NULL OR manager_bot_id <> bot_user_id),
 CHECK (cardinality(added_user_ids) <= 10)
);
CREATE TABLE bot_api_instances (
 bot_user_id BIGINT NOT NULL REFERENCES bots(bot_user_id) ON DELETE CASCADE,
 instance_id TEXT NOT NULL,
 cloud BOOLEAN NOT NULL,
 started_at TIMESTAMPTZ NOT NULL,
 closed_at TIMESTAMPTZ,
 logged_out_at TIMESTAMPTZ,
 PRIMARY KEY(bot_user_id, instance_id)
);
