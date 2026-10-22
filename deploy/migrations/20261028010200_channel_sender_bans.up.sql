CREATE TABLE channel_sender_bans (
    channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    sender_channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    banned_by bigint NOT NULL REFERENCES users(id),
    banned_at integer NOT NULL,
    PRIMARY KEY (channel_id, sender_channel_id)
);
