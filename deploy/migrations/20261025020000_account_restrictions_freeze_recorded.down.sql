ALTER TABLE account_restrictions
    DROP CONSTRAINT IF EXISTS account_restrictions_freeze_recorded_check,
    DROP COLUMN IF EXISTS freeze_recorded;
