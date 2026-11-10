-- 频道导入：任务（channel_imports）、来源→本地消息 id 映射（channel_import_message_map）、
-- 来源文件→本地媒体映射（channel_import_media）。

-- name: InsertChannelImport :one
INSERT INTO channel_imports (channel_id, source_channel_id, source_username, sender_user_id)
VALUES (@channel_id, @source_channel_id, @source_username, @sender_user_id)
RETURNING id, channel_id, state, source_channel_id, source_username, sender_user_id, max_source_id,
          EXTRACT(EPOCH FROM created_at)::bigint AS created_at,
          COALESCE(EXTRACT(EPOCH FROM finalized_at), 0)::bigint AS finalized_at;

-- name: GetChannelImport :one
SELECT id, channel_id, state, source_channel_id, source_username, sender_user_id, max_source_id,
       EXTRACT(EPOCH FROM created_at)::bigint AS created_at,
       COALESCE(EXTRACT(EPOCH FROM finalized_at), 0)::bigint AS finalized_at
FROM channel_imports
WHERE id = @id;

-- name: GetChannelImportForUpdate :one
-- 批量写入先锁任务行：同一个任务的两批写入排队，映射表与 max_source_id 不会交错。
SELECT id, channel_id, state, source_channel_id, source_username, sender_user_id, max_source_id,
       EXTRACT(EPOCH FROM created_at)::bigint AS created_at,
       COALESCE(EXTRACT(EPOCH FROM finalized_at), 0)::bigint AS finalized_at
FROM channel_imports
WHERE id = @id
FOR UPDATE;

-- name: GetActiveChannelImportBySource :one
SELECT id, channel_id, state, source_channel_id, source_username, sender_user_id, max_source_id,
       EXTRACT(EPOCH FROM created_at)::bigint AS created_at,
       COALESCE(EXTRACT(EPOCH FROM finalized_at), 0)::bigint AS finalized_at
FROM channel_imports
WHERE source_channel_id = @source_channel_id AND source_channel_id <> 0 AND state <> 'detached';

-- name: SetChannelImportState :one
UPDATE channel_imports
SET state = @state,
    finalized_at = CASE WHEN @state::text = 'mirroring' AND finalized_at IS NULL THEN now() ELSE finalized_at END
WHERE id = @id
RETURNING id, channel_id, state, source_channel_id, source_username, sender_user_id, max_source_id,
          EXTRACT(EPOCH FROM created_at)::bigint AS created_at,
          COALESCE(EXTRACT(EPOCH FROM finalized_at), 0)::bigint AS finalized_at;

-- name: BumpChannelImportMaxSourceID :exec
UPDATE channel_imports
SET max_source_id = GREATEST(max_source_id, @max_source_id::integer)
WHERE id = @id;

-- name: ListChannelImportMessageMap :many
SELECT source_id, local_id, content_hash
FROM channel_import_message_map
WHERE channel_id = @channel_id AND source_id = ANY(@source_ids::integer[]);

-- name: UpsertChannelImportMessageMap :exec
INSERT INTO channel_import_message_map (channel_id, source_id, local_id, content_hash)
VALUES (@channel_id, @source_id, @local_id, @content_hash)
ON CONFLICT (channel_id, source_id) DO UPDATE SET content_hash = EXCLUDED.content_hash;

-- name: CountChannelImportMessages :one
SELECT count(*)::bigint FROM channel_import_message_map WHERE channel_id = @channel_id;

-- name: GetChannelImportMedia :one
SELECT kind, media_id
FROM channel_import_media
WHERE import_id = @import_id AND source_key = @source_key;

-- name: PutChannelImportMedia :one
-- 两个并发上传同一个文件时先写入的一方胜出，后到的一方读回已有的映射。
INSERT INTO channel_import_media (import_id, source_key, kind, media_id)
VALUES (@import_id, @source_key, @kind, @media_id)
ON CONFLICT (import_id, source_key) DO UPDATE SET source_key = EXCLUDED.source_key
RETURNING kind, media_id;

-- name: CountChannelImportMedia :one
SELECT count(*)::bigint FROM channel_import_media WHERE import_id = @import_id;

-- name: SetImportedChannelMessageCounters :exec
-- 导入的帖子直接带来源的浏览数、转发数和置顶标记；发送路径不写这三列。计数只增不减：
-- 本地读者产生的浏览不会被一次重新同步冲掉。
UPDATE channel_messages
SET views_count = GREATEST(views_count, @views_count::integer),
    forwards_count = GREATEST(forwards_count, @forwards_count::integer),
    pinned = @pinned
WHERE channel_id = @channel_id AND id = @id;

-- name: UpdateImportedChannelMessage :exec
-- 来源编辑过的帖子：正文、实体、媒体、按钮、签名整体替换。
UPDATE channel_messages
SET body = @body,
    entities = @entities,
    media = @media,
    reply_markup = @reply_markup,
    edit_date = @edit_date,
    post_author = @post_author,
    pts = @pts,
    updated_at = now()
WHERE channel_id = @channel_id AND id = @id AND NOT deleted;

-- name: RefreshChannelPinnedMessageID :exec
-- channels.pinned_message_id 是最新一条置顶的 id（channelFull.pinned_msg_id）。
UPDATE channels
SET pinned_message_id = COALESCE((
        SELECT MAX(m.id) FROM channel_messages m
        WHERE m.channel_id = @channel_id AND m.pinned AND NOT m.deleted
    ), 0),
    updated_at = now()
WHERE id = @channel_id;

-- name: SetChannelTopMessageAtLeast :exec
UPDATE channels
SET top_message_id = GREATEST(top_message_id, @top_message_id::integer),
    updated_at = now()
WHERE id = @channel_id;

-- name: MaxChannelMessageID :one
SELECT COALESCE(MAX(id), 0)::integer FROM channel_messages WHERE channel_id = @channel_id;

-- name: AdvanceChannelUpdateFloor :exec
-- 历史回填不写逐条事件：把保留下界推到当前 pts，游标更旧的客户端在 getChannelDifference 里
-- 拿到 tooLong 并重新拉取。latest_event_date 用服务器当前时间，离线账号的 getDifference
-- 才会把这个频道当成有变化。
INSERT INTO channel_update_checkpoints (channel_id, retained_through_pts, latest_event_date, latest_pts)
VALUES (@channel_id, @pts, @event_date, @pts)
ON CONFLICT (channel_id) DO UPDATE SET
    retained_through_pts = GREATEST(channel_update_checkpoints.retained_through_pts, EXCLUDED.retained_through_pts),
    latest_event_date = GREATEST(channel_update_checkpoints.latest_event_date, EXCLUDED.latest_event_date),
    latest_pts = GREATEST(channel_update_checkpoints.latest_pts, EXCLUDED.latest_pts),
    updated_at = now();

-- name: DeleteChannelUpdateEventsThrough :exec
DELETE FROM channel_update_events WHERE channel_id = @channel_id AND pts <= @pts;

-- name: RewriteChannelCreateMessage :execrows
-- 导入到已有的空频道：把“频道已创建”这条服务消息改成来源频道创建时的日期和标题。
UPDATE channel_messages
SET message_date = @message_date,
    action = jsonb_set(action, '{Title}', to_jsonb(@title::text)),
    deleted = @deleted,
    updated_at = now()
WHERE channel_id = @channel_id AND id = @id AND action->>'Type' = 'channel_create';

-- name: SetChannelCreatedDate :exec
UPDATE channels SET date = @date, updated_at = now() WHERE id = @channel_id;

-- name: MarkChannelReadForAllMembers :exec
-- 导入完成时把现有成员的已读位置推到最新一条：回填的历史不算未读。
UPDATE channel_members
SET read_inbox_max_id = GREATEST(read_inbox_max_id, @max_id::integer),
    unread_mark = false,
    updated_at = now()
WHERE channel_id = @channel_id AND status = 'active';
