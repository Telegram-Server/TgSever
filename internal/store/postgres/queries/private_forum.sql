-- name: InsertPrivateForumTopic :exec
INSERT INTO private_forum_topics(owner_user_id,peer_user_id,topic_id,peer_topic_id,name,icon_color,icon_emoji_id,date)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8);

-- name: GetPrivateForumTopic :one
SELECT * FROM private_forum_topics WHERE owner_user_id=$1 AND peer_user_id=$2 AND topic_id=$3 AND NOT deleted;

-- name: ResolvePrivateForumTopic :one
SELECT * FROM private_forum_topics
WHERE ((owner_user_id=sqlc.arg(sender_id) AND peer_user_id=sqlc.arg(recipient_id) AND topic_id=sqlc.arg(topic_id))
 OR (owner_user_id=sqlc.arg(recipient_id) AND peer_user_id=sqlc.arg(sender_id) AND peer_topic_id=sqlc.arg(topic_id)))
 AND NOT deleted;

-- name: UpdatePrivateForumTopic :exec
UPDATE private_forum_topics SET name=$4,icon_emoji_id=$5
WHERE owner_user_id=$1 AND peer_user_id=$2 AND topic_id=$3 AND NOT deleted;

-- name: MarkPrivateForumTopicDeleted :exec
UPDATE private_forum_topics SET deleted=true WHERE owner_user_id=$1 AND peer_user_id=$2 AND topic_id=$3;

-- name: PrivateForumTopicMessageIDs :many
SELECT m.box_id FROM message_boxes m
WHERE m.owner_user_id=sqlc.arg(owner_user_id) AND m.peer_type='user' AND m.peer_id=sqlc.arg(peer_user_id) AND NOT m.deleted
 AND (m.reply_to_top_id=sqlc.arg(topic_id) OR m.box_id=sqlc.arg(topic_id))
ORDER BY m.box_id DESC;

-- name: GetPrivateForumReplyTarget :one
SELECT private_message_id FROM message_boxes
WHERE owner_user_id=sqlc.arg(owner_user_id) AND peer_type='user' AND peer_id=sqlc.arg(peer_id) AND box_id=sqlc.arg(box_id) AND (reply_to_top_id=sqlc.arg(reply_to_top_id) OR box_id=sqlc.arg(reply_to_top_id)) AND NOT deleted;

-- name: CountPrivateForumTopicMessages :one
SELECT count(*) FROM message_boxes WHERE owner_user_id=sqlc.arg(owner_user_id) AND peer_type='user' AND peer_id=sqlc.arg(peer_user_id) AND NOT deleted AND reply_to_top_id=sqlc.arg(topic_id)
 AND (sqlc.arg(max_id)::int=0 OR box_id<sqlc.arg(max_id)) AND (sqlc.arg(min_id)::int=0 OR box_id>sqlc.arg(min_id));

-- name: ListPrivateForumTopicMessageIDs :many
WITH matching AS (
 SELECT box_id,message_date,row_number() OVER (ORDER BY box_id DESC) AS position
 FROM message_boxes WHERE owner_user_id=sqlc.arg(owner_user_id) AND peer_type='user' AND peer_id=sqlc.arg(peer_user_id) AND NOT deleted
 AND reply_to_top_id=sqlc.arg(topic_id)
 AND (sqlc.arg(max_id)::int=0 OR box_id<sqlc.arg(max_id)) AND (sqlc.arg(min_id)::int=0 OR box_id>sqlc.arg(min_id))
), anchor AS (
 SELECT COALESCE(min(position) FILTER (WHERE (sqlc.arg(offset_id)::int=0 OR box_id<sqlc.arg(offset_id)) AND (sqlc.arg(offset_date)::int=0 OR message_date<sqlc.arg(offset_date))),count(*)+1) AS pos FROM matching
)
SELECT box_id FROM matching,anchor WHERE position>=GREATEST(1,anchor.pos+sqlc.arg(add_offset)::int)
ORDER BY box_id DESC LIMIT sqlc.arg(limit_count)::int;
