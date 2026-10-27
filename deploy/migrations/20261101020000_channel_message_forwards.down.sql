DROP TRIGGER IF EXISTS channel_messages_dialog_top_projection_changed ON channel_messages;
CREATE TRIGGER channel_messages_dialog_top_projection_changed
AFTER UPDATE OF
    sender_user_id, from_peer_type, from_peer_id, send_as_peer_type, send_as_peer_id,
    message_date, edit_date, post, silent, noforwards, body, entities, reply_to,
    reply_to_msg_id, reply_to_peer_type, reply_to_peer_id, reply_to_top_id, fwd_from,
    discussion_channel_id, discussion_message_id, action, deleted, views_count, media,
    ttl_period, expires_at, post_author, pinned, via_bot_id, reply_markup,
    from_boosts_applied, rich_message
ON channel_messages
FOR EACH ROW
EXECUTE FUNCTION telesrv_notify_channel_top_message_read_model();

ALTER TABLE channel_messages
    DROP COLUMN IF EXISTS forwards_count;
