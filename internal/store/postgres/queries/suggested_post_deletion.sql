-- name: ListPendingSuggestedPostIDs :many
SELECT suggestion_message_id
FROM suggested_post_approvals
WHERE monoforum_id = $1 AND state IN ('scheduled', 'published')
ORDER BY suggestion_message_id
LIMIT 100;

-- name: GetSuggestedPostPublishedDeletion :one
SELECT deleted, delete_date
FROM channel_messages
WHERE channel_id = sqlc.arg(channel_id) AND id = sqlc.arg(message_id)
FOR SHARE;
