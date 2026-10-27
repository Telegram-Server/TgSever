-- 成员限制的执行人与限制时间（#167）：此前设置限制时把执行的管理员写进 inviter_user_id，
-- 限制一个在群成员会覆盖他的邀请人。现在单独两列记录；限制为空时两列为 0。
ALTER TABLE channel_members
    ADD COLUMN IF NOT EXISTS restricted_by_user_id bigint DEFAULT 0 NOT NULL,
    ADD COLUMN IF NOT EXISTS restricted_at integer DEFAULT 0 NOT NULL;

-- 回填升级前的限制记录，执行人取当时被改写成管理员的 inviter_user_id：
--   被踢出（kicked）、不在群里但受限（banned）：时间取 left_at；
--   在群受限（active 且 banned_rights 有任一限制项为真；只看限制项，不看 UntilDate——只带到期
--   时间的记录没有限制任何事）：当时没有记录时间，保持 0。
-- 只补两列都还为 0 的行，重复执行幂等。
UPDATE channel_members
SET restricted_by_user_id = inviter_user_id,
    restricted_at = CASE WHEN status IN ('kicked', 'banned') THEN left_at ELSE 0 END
WHERE restricted_by_user_id = 0
  AND restricted_at = 0
  AND (
    status IN ('kicked', 'banned')
    OR (
      status = 'active'
      AND EXISTS (
        SELECT 1
        FROM jsonb_each(banned_rights) AS item(key, value)
        WHERE item.key <> 'UntilDate'
          AND item.value NOT IN ('false'::jsonb, '0'::jsonb, 'null'::jsonb)
      )
    )
  );
