-- name: LockReactionChannelMessage :exec
SELECT id FROM channel_messages WHERE channel_id = $1 AND id = $2 FOR UPDATE;

-- name: ListActorChannelReactions :many
SELECT reaction_type, reaction_value FROM channel_message_reactions
WHERE channel_id = $1 AND message_id = $2 AND reacted_user_id = $3
ORDER BY chosen_order;

-- name: ListActorPrivateReactions :many
SELECT reaction_type, reaction_value FROM private_message_reactions
WHERE message_sender_id = $1 AND private_message_id = $2 AND user_id = $3
ORDER BY chosen_order;

-- name: IsPrivateReactionServiceMessage :one
SELECT COALESCE(media->>'kind' = 'service', false)::boolean FROM message_boxes
WHERE owner_user_id = $1 AND box_id = $2;

-- name: LockReactionChannelMessages :exec
SELECT id FROM channel_messages
WHERE channel_id = sqlc.arg(channel_id) AND id = ANY(sqlc.arg(message_ids)::int[])
ORDER BY id FOR UPDATE;

-- name: ListActorReactionsForMessages :many
SELECT message_id, reaction_type, reaction_value FROM channel_message_reactions
WHERE channel_id = sqlc.arg(channel_id) AND reacted_user_id = sqlc.arg(user_id)
  AND message_id = ANY(sqlc.arg(message_ids)::int[])
ORDER BY message_id, chosen_order;
