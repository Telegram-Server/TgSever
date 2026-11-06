-- 新账号默认入驻：入驻模板（account_onboarding_defaults + 两张名单表）与入驻任务（account_onboarding_jobs）。

-- name: GetOnboardingDefaults :one
-- 模板行与两份名单在一条语句里读出，同一个快照：不会和一次并发的整体替换交错。
SELECT d.updated_at, d.command_id,
       ARRAY(SELECT c.contact_user_id FROM account_onboarding_default_contacts c ORDER BY c.position)::bigint[] AS contact_user_ids,
       ARRAY(SELECT c.mutual FROM account_onboarding_default_contacts c ORDER BY c.position)::boolean[] AS contact_mutual,
       ARRAY(SELECT ch.channel_id FROM account_onboarding_default_channels ch ORDER BY ch.position)::bigint[] AS channel_ids
FROM account_onboarding_defaults d;

-- name: LockOnboardingDefaultsTable :exec
-- 整体替换先锁住模板表：还没有模板行时 FOR UPDATE 锁不住任何东西，两次首次设置要靠表锁排队。
-- SHARE ROW EXCLUSIVE 与自身互斥，不挡普通读取。
LOCK TABLE account_onboarding_defaults IN SHARE ROW EXCLUSIVE MODE;

-- name: UpsertOnboardingDefaults :one
INSERT INTO account_onboarding_defaults (singleton, updated_at, command_id)
VALUES (true, now(), @command_id::text)
ON CONFLICT (singleton) DO UPDATE SET
    updated_at = EXCLUDED.updated_at,
    command_id = EXCLUDED.command_id
RETURNING updated_at;

-- name: DeleteOnboardingDefaultContacts :exec
DELETE FROM account_onboarding_default_contacts;

-- name: InsertOnboardingDefaultContacts :exec
INSERT INTO account_onboarding_default_contacts (contact_user_id, position, mutual)
SELECT c.contact_user_id, c.ord::integer, (@mutual::boolean[])[c.ord]
FROM unnest(@contact_user_ids::bigint[]) WITH ORDINALITY AS c(contact_user_id, ord);

-- name: DeleteOnboardingDefaultChannels :exec
DELETE FROM account_onboarding_default_channels;

-- name: AdminChannelSummaries :many
-- 一组群和频道的展示信息（后台入驻模板页用）；不存在的 id 没有行，由调用方补上。
SELECT c.id, c.title,
       COALESCE(NULLIF(c.username, ''), p.username_lower, '')::text AS display_username,
       c.broadcast, c.megagroup, c.deleted
FROM channels c
LEFT JOIN peer_usernames p ON p.peer_type = 'channel' AND p.peer_id = c.id AND p.editable
WHERE c.id = ANY(@ids::bigint[]);

-- name: EnqueueOnboardingJob :exec
-- 建号事务里按当时的模板为新账号写一条任务；没有模板或模板为空时不写。模板表锁（整体替换时
-- 持有）与这里的读取互不阻塞：读到的是事务开始后最近一次提交的模板。
INSERT INTO account_onboarding_jobs (user_id, contact_user_ids, contact_mutual, channel_ids)
SELECT @user_id::bigint,
       ARRAY(SELECT c.contact_user_id FROM account_onboarding_default_contacts c ORDER BY c.position)::bigint[],
       ARRAY(SELECT c.mutual FROM account_onboarding_default_contacts c ORDER BY c.position)::boolean[],
       ARRAY(SELECT ch.channel_id FROM account_onboarding_default_channels ch ORDER BY ch.position)::bigint[]
WHERE EXISTS (SELECT 1 FROM account_onboarding_default_contacts)
   OR EXISTS (SELECT 1 FROM account_onboarding_default_channels);

-- name: ClaimOnboardingJobs :many
-- 领取到期的任务并把 available_at 推到租约结束；SKIP LOCKED 让并发的领取者互不等待、不重复领取。
UPDATE account_onboarding_jobs j
SET available_at = now() + make_interval(secs => @lease_seconds::double precision)
WHERE j.user_id IN (
    SELECT c.user_id FROM account_onboarding_jobs c
    WHERE c.status = 'pending' AND c.available_at <= now()
    ORDER BY c.created_at, c.user_id
    LIMIT @max_jobs::integer
    FOR UPDATE SKIP LOCKED
)
RETURNING j.user_id, j.contact_user_ids, j.contact_mutual, j.channel_ids, j.attempts, j.last_error, j.created_at;

-- name: DeleteOnboardingJob :exec
DELETE FROM account_onboarding_jobs WHERE user_id = @user_id::bigint;

-- name: RetryOnboardingJob :exec
UPDATE account_onboarding_jobs
SET attempts = attempts + 1,
    last_error = @last_error::text,
    available_at = now() + make_interval(secs => @retry_after_seconds::double precision)
WHERE user_id = @user_id::bigint;

-- name: AbandonOnboardingJob :exec
UPDATE account_onboarding_jobs
SET attempts = attempts + 1,
    last_error = @last_error::text,
    status = 'abandoned'
WHERE user_id = @user_id::bigint;

-- name: InsertOnboardingDefaultChannels :exec
INSERT INTO account_onboarding_default_channels (channel_id, position)
SELECT ch.channel_id, ch.ord::integer
FROM unnest(@channel_ids::bigint[]) WITH ORDINALITY AS ch(channel_id, ord);
