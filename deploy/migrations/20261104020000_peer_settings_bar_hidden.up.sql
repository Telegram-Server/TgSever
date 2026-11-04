-- 会话提示栏（添加联系人 / 分享手机号……）被关闭的记录。原先只记在 dialogs 行上，
-- 从联系人列表点进还没聊过的人时没有对话行，关闭就存不下来，提示栏会反复出现。
-- 本表是唯一读取来源；dialogs.hidden_peer_settings_bar 仍同步写入，只供对话列表 hash 使用。
CREATE TABLE public.peer_settings_bar_hidden (
    user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    peer_type text NOT NULL,
    peer_id bigint NOT NULL,
    hidden_at timestamp with time zone DEFAULT now() NOT NULL,
    PRIMARY KEY (user_id, peer_type, peer_id)
);

INSERT INTO public.peer_settings_bar_hidden (user_id, peer_type, peer_id)
SELECT d.user_id, d.peer_type, d.peer_id
FROM public.dialogs d
JOIN public.users u ON u.id = d.user_id
WHERE d.hidden_peer_settings_bar
ON CONFLICT DO NOTHING;
