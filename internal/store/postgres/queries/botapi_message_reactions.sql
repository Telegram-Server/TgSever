-- name: ResolvePrivateReactionAlbumTarget :one
SELECT candidate.box_id FROM message_boxes source
JOIN message_boxes candidate ON candidate.owner_user_id=source.owner_user_id
 AND candidate.peer_type=source.peer_type AND candidate.peer_id=source.peer_id
 AND ((source.grouped_id<>0 AND candidate.grouped_id=source.grouped_id) OR (source.grouped_id=0 AND candidate.box_id=source.box_id))
WHERE source.owner_user_id=sqlc.arg(owner_user_id)::bigint AND source.box_id=sqlc.arg(box_id)::int
 AND source.peer_type='user' AND source.peer_id=sqlc.arg(peer_id)::bigint AND NOT candidate.deleted
ORDER BY candidate.box_id LIMIT 1;

-- name: ResolveChannelReactionAlbumTarget :one
SELECT candidate.id FROM channel_messages source
JOIN channel_messages candidate ON candidate.channel_id=source.channel_id
 AND ((source.grouped_id<>0 AND candidate.grouped_id=source.grouped_id) OR (source.grouped_id=0 AND candidate.id=source.id))
WHERE source.channel_id=sqlc.arg(channel_id)::bigint AND source.id=sqlc.arg(id)::int AND NOT candidate.deleted
ORDER BY candidate.id LIMIT 1;

-- name: HasChannelReactionValue :one
SELECT EXISTS(SELECT 1 FROM channel_message_reactions WHERE channel_id=sqlc.arg(channel_id)::bigint
 AND message_id=sqlc.arg(message_id)::int AND reaction_type=sqlc.arg(reaction_type)::text AND reaction_value=sqlc.arg(reaction_value)::text);

-- name: LockBotReplyChannelSource :one
SELECT id FROM channel_messages WHERE channel_id=sqlc.arg(channel_id)::bigint AND id=sqlc.arg(id)::int AND NOT deleted FOR SHARE;

-- name: IsBotReplyPrivateProtected :one
SELECT EXISTS(SELECT 1 FROM private_no_forwards_chats WHERE user_low_id=sqlc.arg(user_low_id)::bigint AND user_high_id=sqlc.arg(user_high_id)::bigint AND COALESCE(enabled_by_user_id,0)<>0);
