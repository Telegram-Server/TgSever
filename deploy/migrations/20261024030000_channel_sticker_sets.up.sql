ALTER TABLE channels ADD COLUMN sticker_set_id BIGINT NOT NULL DEFAULT 0 CHECK (sticker_set_id >= 0);
