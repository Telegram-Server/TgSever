ALTER TABLE bots
    ADD COLUMN group_admin_rights jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN broadcast_admin_rights jsonb NOT NULL DEFAULT '{}'::jsonb;
