ALTER TABLE account_restrictions
    DROP CONSTRAINT IF EXISTS account_restrictions_device_limit_check,
    DROP COLUMN IF EXISTS device_limit;
