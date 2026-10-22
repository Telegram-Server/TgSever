CREATE TABLE private_forum_topics (
 owner_user_id bigint NOT NULL REFERENCES users(id),
 peer_user_id bigint NOT NULL REFERENCES users(id),
 topic_id integer NOT NULL CHECK (topic_id > 0),
 peer_topic_id integer NOT NULL CHECK (peer_topic_id > 0),
 name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
 icon_color integer NOT NULL,
 icon_emoji_id bigint NOT NULL DEFAULT 0,
 date integer NOT NULL,
 deleted boolean NOT NULL DEFAULT false,
 PRIMARY KEY (owner_user_id,peer_user_id,topic_id),
 CHECK (owner_user_id <> peer_user_id)
);
CREATE INDEX message_boxes_private_topic_idx ON message_boxes(owner_user_id,peer_id,reply_to_top_id,box_id) WHERE peer_type='user' AND NOT deleted AND reply_to_top_id>0;
CREATE UNIQUE INDEX private_forum_peer_topic_idx ON private_forum_topics(peer_user_id,owner_user_id,peer_topic_id);
