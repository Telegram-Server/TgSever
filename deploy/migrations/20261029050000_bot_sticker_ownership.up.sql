ALTER TABLE sticker_sets ADD COLUMN creator_bot_id bigint NOT NULL DEFAULT 0;
CREATE INDEX sticker_sets_creator_bot_idx ON sticker_sets (creator_bot_id) WHERE creator_bot_id <> 0 AND deleted = false;
