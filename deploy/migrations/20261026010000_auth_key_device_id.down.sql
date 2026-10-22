ALTER TABLE auth_keys
    DROP CONSTRAINT IF EXISTS auth_keys_device_id_check,
    DROP COLUMN IF EXISTS device_id;
