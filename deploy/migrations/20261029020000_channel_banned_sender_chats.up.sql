-- 频道身份封禁（#137）：记录"某个频道 peer 在某个群/频道里被封禁"，阻止任何人以该频道身份
-- 在群里发言。只有全封一种状态（解封即删除该行），所以不存权限位。
-- 封禁/解封都会让群成员列表（kicked 筛选）变化，触发器顺带 bump channel_participants 读模型版本，
-- 让各实例的成员列表缓存失效。
CREATE TABLE channel_banned_sender_chats (
    channel_id        BIGINT  NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    sender_chat_id    BIGINT  NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    banned_by_user_id BIGINT  NOT NULL,
    banned_at         INTEGER NOT NULL,
    PRIMARY KEY (channel_id, sender_chat_id),
    CONSTRAINT channel_banned_sender_chats_self_check CHECK (channel_id <> sender_chat_id)
);

-- 被封频道被硬删时由外键级联删除，需要按 sender_chat_id 查找。
CREATE INDEX channel_banned_sender_chats_sender_chat_idx ON channel_banned_sender_chats (sender_chat_id);

CREATE FUNCTION telesrv_notify_channel_banned_sender_chats_read_model() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM telesrv_bump_read_model_version('channel_participants', 0, 'channel', OLD.channel_id);
    ELSE
        PERFORM telesrv_bump_read_model_version('channel_participants', 0, 'channel', NEW.channel_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER channel_banned_sender_chats_read_model
    AFTER INSERT OR DELETE ON channel_banned_sender_chats
    FOR EACH ROW EXECUTE FUNCTION telesrv_notify_channel_banned_sender_chats_read_model();
