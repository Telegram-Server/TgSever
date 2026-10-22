-- Preserve the oldest active main link for each channel/admin. Parameterless
-- additional links were previously misclassified as main links.
WITH ranked AS (
    SELECT channel_id, invite_id,
           row_number() OVER (
               PARTITION BY channel_id, admin_user_id
               ORDER BY created_at, hash, invite_id
           ) AS position
    FROM channel_invites
    WHERE permanent AND NOT revoked
)
UPDATE channel_invites AS invite
SET permanent = false, updated_at = now()
FROM ranked
WHERE invite.channel_id = ranked.channel_id
  AND invite.invite_id = ranked.invite_id
  AND ranked.position > 1;

CREATE UNIQUE INDEX channel_invites_one_active_permanent
ON channel_invites (channel_id, admin_user_id)
WHERE permanent AND NOT revoked;
