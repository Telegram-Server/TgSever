CREATE TABLE bot_story_owners (
    owner_peer_type text NOT NULL,
    owner_peer_id bigint NOT NULL,
    story_id integer NOT NULL,
    bot_user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    business_connection_id text NOT NULL,
    PRIMARY KEY (owner_peer_type, owner_peer_id, story_id),
    FOREIGN KEY (owner_peer_type, owner_peer_id, story_id)
        REFERENCES stories(owner_peer_type, owner_peer_id, story_id) ON DELETE CASCADE
);
