-- 频道导入：把外部来源（官方 Telegram 的频道）的历史克隆进一个本地广播频道，并在之后持续镜像。
--
-- channel_imports：一个本地频道至多一个导入任务。state：
--   importing  历史回填中：批量写入不产生逐条 update 事件；
--   mirroring  回填完成：新帖、编辑、删除按正常事件下发；
--   detached   不再镜像，频道是一个普通频道（映射表保留，供查询）。
-- sender_user_id 是导入帖子的 sender（channel_messages.sender_user_id 不可为空且有外键）。
-- max_source_id 是已写入的最大来源消息 id，供克隆程序断点续传。
CREATE TABLE channel_imports (
    id                BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    channel_id        BIGINT      NOT NULL UNIQUE REFERENCES channels(id) ON DELETE CASCADE,
    state             TEXT        NOT NULL DEFAULT 'importing' CHECK (state IN ('importing', 'mirroring', 'detached')),
    source_channel_id BIGINT      NOT NULL DEFAULT 0,
    source_username   TEXT        NOT NULL DEFAULT '',
    sender_user_id    BIGINT      NOT NULL REFERENCES users(id),
    max_source_id     INTEGER     NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    finalized_at      TIMESTAMPTZ
);

-- 同一个来源频道同时只有一个在用的任务：重复创建返回已有任务，而不是克隆出第二个频道。
CREATE UNIQUE INDEX channel_imports_source_idx ON channel_imports (source_channel_id)
    WHERE source_channel_id <> 0 AND state <> 'detached';

-- 来源消息 id → 本地消息 id。回填阶段两者相等；本地发过帖之后来源的新帖拿到的是本地分配的 id，
-- 之后来源的编辑、删除、回复引用都经这张表换算。content_hash 让重发同一条消息成为空操作。
CREATE TABLE channel_import_message_map (
    channel_id   BIGINT  NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    source_id    INTEGER NOT NULL,
    local_id     INTEGER NOT NULL,
    content_hash BYTEA   NOT NULL,
    PRIMARY KEY (channel_id, source_id)
);

CREATE UNIQUE INDEX channel_import_message_map_local_idx ON channel_import_message_map (channel_id, local_id);

-- 来源文件标识 → 已创建的本地媒体。克隆程序断点重传时同一个文件不再上传第二次。
CREATE TABLE channel_import_media (
    import_id  BIGINT NOT NULL REFERENCES channel_imports(id) ON DELETE CASCADE,
    source_key TEXT   NOT NULL,
    kind       TEXT   NOT NULL CHECK (kind IN ('photo', 'document')),
    media_id   BIGINT NOT NULL,
    PRIMARY KEY (import_id, source_key)
);
