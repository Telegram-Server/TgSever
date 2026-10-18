-- 消息删除记录：每个被删的消息副本一行，写明谁删的、何时删的、删的是哪条。
-- 与删除操作同事务写入，永久保留；snapshot 是删除时刻的原始行。
CREATE SEQUENCE message_deletion_batch_seq;

CREATE TABLE message_deletions (
	id bigserial PRIMARY KEY,
	-- batch_id 把同一次删除操作产生的所有行归为一组。
	batch_id bigint NOT NULL,
	deleted_date integer NOT NULL,
	source varchar(32) NOT NULL,
	scope varchar(16) NOT NULL,
	actor_type varchar(16) NOT NULL,
	actor_user_id bigint NOT NULL DEFAULT 0,
	operator_id text NOT NULL DEFAULT '',
	command_id text NOT NULL DEFAULT '',
	reason text NOT NULL DEFAULT '',
	origin_auth_key_id bigint NOT NULL DEFAULT 0,
	origin_session_id bigint NOT NULL DEFAULT 0,
	origin_device jsonb NOT NULL DEFAULT '{}'::jsonb,
	-- owner_user_id 是被删副本所属账号；群消息没有逐人副本，取 0。
	owner_user_id bigint NOT NULL DEFAULT 0,
	peer_type varchar(16) NOT NULL,
	peer_id bigint NOT NULL,
	message_id integer NOT NULL,
	private_message_id bigint NOT NULL DEFAULT 0,
	sender_user_id bigint NOT NULL,
	message_date integer NOT NULL,
	snapshot jsonb NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	CONSTRAINT message_deletions_source_check CHECK (source IN (
		'user', 'history_clear', 'saved_history', 'topic_history', 'participant_history',
		'discussion_cascade', 'ttl', 'moderation', 'admin', 'bot'
	)),
	CONSTRAINT message_deletions_scope_check CHECK (scope IN ('self', 'both', 'everyone')),
	CONSTRAINT message_deletions_actor_type_check CHECK (actor_type IN ('user', 'bot', 'system', 'operator'))
);

CREATE INDEX message_deletions_actor_idx ON message_deletions (actor_user_id, id DESC);
CREATE INDEX message_deletions_owner_peer_idx ON message_deletions (owner_user_id, peer_id, id DESC);
CREATE INDEX message_deletions_peer_idx ON message_deletions (peer_type, peer_id, id DESC);
CREATE INDEX message_deletions_sender_idx ON message_deletions (sender_user_id, id DESC);
