-- #38 之后 updateProfilePhoto(InputPhoto) 只接受目标 owner 头像历史里的照片；
-- 新的建议头像在消息送达后写入收件人的停用历史行（OfferProfilePhoto）。
-- 本迁移为上线前已经送达的建议补同样形状的停用行，否则收件人点旧消息的
-- "设为头像" 会得到 PHOTO_ID_INVALID。
--
-- 只取收件人仍然可见的那份 box：
--   * 建议者自己的 outgoing 副本不参与（owner = recipient），建议者不能借此用这张照片；
--   * 收件人当时拉黑了建议者时消息没有投递，不存在收件人 box，与运行时的跳过规则一致；
--   * 收件人已删除的消息（deleted）没有可点的入口，不补。
-- active = false、sort_order = 0：不改变任何人的当前头像，也不进入可见相册；
-- 已有同一行（例如已接受过的建议）时保持原状。
INSERT INTO profile_photos (owner_peer_type, owner_peer_id, kind, photo_id, date, active, sort_order)
SELECT DISTINCT ON (p.recipient_user_id, (p.media -> 'service_action' -> 'photo' ->> 'id')::bigint)
    'user',
    p.recipient_user_id,
    'profile',
    (p.media -> 'service_action' -> 'photo' ->> 'id')::bigint,
    p.message_date,
    false,
    0
FROM private_messages AS p
JOIN message_boxes AS b
  ON b.private_message_id = p.id
 AND b.owner_user_id = p.recipient_user_id
WHERE p.media ->> 'kind' = 'service'
  AND p.media -> 'service_action' ->> 'kind' = 'suggest_profile_photo'
  AND (p.media -> 'service_action' -> 'photo' ->> 'id')::bigint <> 0
  AND p.sender_user_id <> p.recipient_user_id
  AND NOT b.outgoing
  AND NOT b.deleted
ORDER BY p.recipient_user_id, (p.media -> 'service_action' -> 'photo' ->> 'id')::bigint, p.message_date
ON CONFLICT (owner_peer_type, owner_peer_id, kind, photo_id) DO NOTHING;
