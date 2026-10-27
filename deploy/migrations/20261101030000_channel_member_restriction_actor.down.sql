ALTER TABLE channel_members
    DROP COLUMN IF EXISTS restricted_at,
    DROP COLUMN IF EXISTS restricted_by_user_id;
