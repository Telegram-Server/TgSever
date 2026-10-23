-- bot qts 序列（#145）。

-- name: AllocateBotUpdateQts :one
-- 在调用方的事务里为 bot 分配下一个 qts；行锁让同一 bot 的并发写入排队，qts 连续。
-- 初始 qts 是 1（domain.BotInitialQts），第一条记录分配 2。
INSERT INTO bot_update_states (bot_user_id, qts)
VALUES ($1, 2)
ON CONFLICT (bot_user_id) DO UPDATE SET qts = bot_update_states.qts + 1
RETURNING qts;

-- name: InsertBotUpdate :exec
INSERT INTO bot_updates (bot_user_id, qts, date, payload)
VALUES ($1, $2, $3, $4);

-- name: ListBotUpdates :many
SELECT bot_user_id, qts, date, payload
FROM bot_updates
WHERE bot_user_id = $1 AND qts > $2
ORDER BY qts
LIMIT $3;

-- name: GetBotUpdatesState :one
SELECT
    COALESCE((SELECT s.qts FROM bot_update_states s WHERE s.bot_user_id = $1), 1)::integer AS qts,
    COALESCE((SELECT min(u.qts) FROM bot_updates u WHERE u.bot_user_id = $1), 0)::integer AS min_retained_qts;

-- name: DeleteExpiredBotUpdates :execrows
DELETE FROM bot_updates
WHERE (bot_user_id, qts) IN (
    SELECT e.bot_user_id, e.qts
    FROM bot_updates e
    WHERE e.date < $1
    ORDER BY e.date
    LIMIT $2
);

-- name: ListChannelAdminBotIDs :many
-- 源事件的事务里列出群里的管理员 bot（在群的群主与管理员，domain.IsChannelAdminBotAudience）：
-- 成员变动（#146）、入群申请（#149）、消息反应（#150）的收件人候选，各自的领域规则再细分。
SELECT m.user_id
FROM channel_members m
JOIN users u ON u.id = m.user_id
WHERE m.channel_id = $1
  AND m.status = 'active'
  AND m.role IN ('creator', 'admin')
  AND u.is_bot
ORDER BY m.user_id;

-- name: FilterBotUserIDs :many
-- 成员变动的事务里判定哪些主体是 bot 账号。
SELECT id
FROM users
WHERE id = ANY(@user_ids::bigint[]) AND is_bot;
