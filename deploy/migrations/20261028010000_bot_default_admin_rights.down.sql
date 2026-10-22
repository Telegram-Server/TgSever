ALTER TABLE bots
    DROP COLUMN IF EXISTS broadcast_admin_rights,
    DROP COLUMN IF EXISTS group_admin_rights;
