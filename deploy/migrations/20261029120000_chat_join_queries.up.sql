CREATE TABLE channel_guard_bots (
 channel_id bigint PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
 bot_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE chat_join_queries (
 id bigint PRIMARY KEY CHECK (id > 0),
 channel_id bigint NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
 bot_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 request_date integer NOT NULL,
 expires_at integer NOT NULL,
 state text NOT NULL CHECK (state IN ('pending','webview','approve','decline','queue')),
 web_app_url text NOT NULL DEFAULT '',
 UNIQUE(channel_id,user_id,request_date)
);
CREATE INDEX chat_join_queries_expiration ON chat_join_queries(expires_at);
