DROP INDEX IF EXISTS channel_invites_one_active_permanent;
-- The data repair is intentionally not undone: a corrected additional link
-- cannot be distinguished from one created after this migration.
