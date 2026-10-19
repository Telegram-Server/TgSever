-- name: LockCommunityBanChannelMembers :many
-- 社区封禁前按 (channel_id, user_id) 顺序锁住每个关联频道里操作者与目标的成员行，
-- 管理员保护检查与随后的封禁在同一事务、同一组行锁内完成。
SELECT cm.channel_id, cm.user_id, cm.inviter_user_id, cm.role, cm.status, c.creator_user_id
FROM community_peer_links l
JOIN channels c ON c.id = l.peer_id
JOIN channel_members cm ON cm.channel_id = l.peer_id
WHERE l.community_id = @community_id
  AND l.peer_type = 'channel'
  AND cm.user_id = ANY(@user_ids::bigint[])
ORDER BY cm.channel_id, cm.user_id
FOR UPDATE OF cm;
