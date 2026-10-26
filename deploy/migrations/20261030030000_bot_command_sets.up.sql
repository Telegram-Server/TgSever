-- bots.setBotCommands 按 scope + lang_code 分桶存储命令（Bot API setMyCommands 的
-- scope / language_code）。default scope + 空 lang_code 这一桶仍存在 bots.commands：
-- 内置 bot 的迁移直接写这一列，BotFather /setcommands 也只管它，不必回填。
--
-- 只存非空的桶：setBotCommands 传空列表 = 删除该桶，查看者回退到更宽的 scope。
-- peer 指向 bot 视角的会话：私聊是 ('user', 对方 user id)，群是 ('channel', 频道 id)
-- （基础群在本服务里也是频道）。

CREATE TABLE public.bot_command_sets (
    bot_user_id bigint NOT NULL REFERENCES public.bots(bot_user_id) ON DELETE CASCADE,
    scope text NOT NULL,
    peer_type text NOT NULL DEFAULT '',
    peer_id bigint NOT NULL DEFAULT 0,
    user_id bigint NOT NULL DEFAULT 0,
    lang_code text NOT NULL DEFAULT '',
    commands jsonb NOT NULL,
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT bot_command_sets_pkey PRIMARY KEY (bot_user_id, scope, peer_type, peer_id, user_id, lang_code),
    CONSTRAINT bot_command_sets_scope_check CHECK (
        (scope IN ('default', 'users', 'chats', 'chat_admins') AND peer_type = '' AND peer_id = 0 AND user_id = 0) OR
        (scope = 'peer' AND peer_type IN ('user', 'channel') AND peer_id > 0 AND user_id = 0) OR
        (scope = 'peer_admins' AND peer_type = 'channel' AND peer_id > 0 AND user_id = 0) OR
        (scope = 'peer_user' AND peer_type = 'channel' AND peer_id > 0 AND user_id > 0)
    ),
    CONSTRAINT bot_command_sets_lang_check CHECK (lang_code ~ '^([a-z]{2})?$'),
    -- default + 空 lang 在 bots.commands，不在这里。
    CONSTRAINT bot_command_sets_default_lang_check CHECK (scope <> 'default' OR lang_code <> ''),
    CONSTRAINT bot_command_sets_commands_check CHECK (
        jsonb_typeof(commands) = 'array' AND jsonb_array_length(commands) > 0
    )
);

-- 读路径按 (bot, 会话) 取与该会话相关的桶。
CREATE INDEX bot_command_sets_peer_idx
    ON public.bot_command_sets (bot_user_id, peer_type, peer_id)
    WHERE peer_id <> 0;
