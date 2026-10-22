ALTER TABLE business_connected_bots ADD COLUMN connection_id text NOT NULL DEFAULT '';
UPDATE business_connected_bots SET connection_id = replace(gen_random_uuid()::text, '-', '');
ALTER TABLE business_connected_bots ALTER COLUMN connection_id DROP DEFAULT;
CREATE UNIQUE INDEX business_connected_bots_connection_idx ON business_connected_bots (connection_id);
CREATE TABLE business_bot_connections (
    connection_id text PRIMARY KEY,
    owner_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    bot_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    snapshot jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX business_bot_connections_owner_idx ON business_bot_connections(owner_user_id);
INSERT INTO business_bot_connections(connection_id, owner_user_id, bot_user_id, snapshot)
SELECT connection_id, owner_user_id, bot_user_id, jsonb_build_object(
    'ConnectionID', connection_id, 'OwnerUserID', owner_user_id, 'BotUserID', bot_user_id,
    'Recipients', recipients, 'Rights', rights,
    'CreatedAtUnix', extract(epoch from created_at)::bigint,
    'UpdatedAtUnix', extract(epoch from updated_at)::bigint, 'Disabled', false)
FROM business_connected_bots;
