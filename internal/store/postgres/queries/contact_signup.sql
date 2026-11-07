-- 新账号默认入驻写的 messageActionContactSignUp 服务消息（contact_sign_up_notices 回执）。

-- name: GetContactSignUpNoticeBoxID :one
SELECT box_id FROM contact_sign_up_notices
WHERE owner_user_id = @owner_user_id::bigint AND from_user_id = @from_user_id::bigint;

-- name: InsertContactSignUpNotice :exec
INSERT INTO contact_sign_up_notices (owner_user_id, from_user_id, box_id)
VALUES (@owner_user_id::bigint, @from_user_id::bigint, @box_id::integer);

-- name: ContactSignUpNoticeEligible :one
-- Owner 与 From 都存在且未注销，Owner 不是 bot。调用方先锁住 Owner 的 users 行。
SELECT (
  EXISTS (SELECT 1 FROM users o WHERE o.id = @owner_user_id::bigint AND o.deleted_at IS NULL AND NOT o.is_bot)
  AND EXISTS (SELECT 1 FROM users f WHERE f.id = @from_user_id::bigint AND f.deleted_at IS NULL)
)::boolean AS eligible;
