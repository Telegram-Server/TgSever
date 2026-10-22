CREATE TABLE bot_guest_queries (
 id bigint PRIMARY KEY CHECK (id <> 0),
 bot_user_id bigint NOT NULL REFERENCES users(id),
 caller_user_id bigint NOT NULL REFERENCES users(id),
 peer_type text NOT NULL CHECK (peer_type IN ('user','channel')),
 peer_id bigint NOT NULL CHECK (peer_id > 0),
 message_id integer NOT NULL CHECK (message_id > 0),
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK (expires_at > created_at)
);
CREATE INDEX bot_guest_queries_expiry ON bot_guest_queries(expires_at);
ALTER TABLE webview_requested_buttons
 ADD COLUMN style text NOT NULL DEFAULT '',
 ADD COLUMN icon_custom_emoji_id bigint NOT NULL DEFAULT 0,
 ADD COLUMN suggested_name text NOT NULL DEFAULT '',
 ADD COLUMN suggested_username text NOT NULL DEFAULT '';
ALTER TABLE bots ADD COLUMN inline_feedback_percentage integer NOT NULL DEFAULT 0 CHECK (inline_feedback_percentage BETWEEN 0 AND 100);