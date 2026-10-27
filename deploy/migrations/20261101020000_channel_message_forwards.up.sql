-- 频道帖子转发数（#166）：转发写入时在同一事务里原子自增，只计 broadcast post。
ALTER TABLE channel_messages
    ADD COLUMN forwards_count integer DEFAULT 0 NOT NULL;

-- 转发数和浏览数一样出现在会话列表的顶部消息里：变化时同样让 channel_base 读模型失效。
DROP TRIGGER IF EXISTS channel_messages_dialog_top_projection_changed ON channel_messages;
CREATE TRIGGER channel_messages_dialog_top_projection_changed
AFTER UPDATE OF
    sender_user_id, from_peer_type, from_peer_id, send_as_peer_type, send_as_peer_id,
    message_date, edit_date, post, silent, noforwards, body, entities, reply_to,
    reply_to_msg_id, reply_to_peer_type, reply_to_peer_id, reply_to_top_id, fwd_from,
    discussion_channel_id, discussion_message_id, action, deleted, views_count, forwards_count, media,
    ttl_period, expires_at, post_author, pinned, via_bot_id, reply_markup,
    from_boosts_applied, rich_message
ON channel_messages
FOR EACH ROW
EXECUTE FUNCTION telesrv_notify_channel_top_message_read_model();
