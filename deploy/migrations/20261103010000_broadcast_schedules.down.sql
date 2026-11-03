DROP INDEX IF EXISTS broadcasts_schedule_idx;
ALTER TABLE broadcasts DROP COLUMN IF EXISTS schedule_id;
DROP TABLE IF EXISTS broadcast_schedules;
