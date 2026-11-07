-- 新账号默认入驻写的 messageActionContactSignUp 服务消息的回执：每对 (owner, from) 只写一次，
-- 入驻任务重跑时按它重放第一次写入的消息。消息本体在 message_boxes / private_messages，
-- 回执在同一事务里写入。账号真正删除行时级联删除。
CREATE TABLE contact_sign_up_notices (
    owner_user_id BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    from_user_id  BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    box_id        INTEGER     NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_user_id, from_user_id)
);
