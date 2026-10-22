-- name: InsertBotStoryOwner :exec
INSERT INTO bot_story_owners (owner_peer_type, owner_peer_id, story_id, bot_user_id, business_connection_id)
VALUES ($1, $2, $3, $4, $5);

-- name: GetBotStoryOwner :one
SELECT bot_user_id, business_connection_id
FROM bot_story_owners
WHERE owner_peer_type = $1 AND owner_peer_id = $2 AND story_id = $3;
