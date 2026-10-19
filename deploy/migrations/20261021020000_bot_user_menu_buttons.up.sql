-- 机器人对单个用户的专属菜单按钮（bots.setBotMenuButton 的 user_id / Bot API setChatMenuButton 的 chat_id）。
-- 列与 bots.menu_button_* 一致；没有行即回落到 bots 表的全局默认。default 类型不落行（写 default = 删除）。
CREATE TABLE bot_user_menu_buttons (
    bot_user_id BIGINT NOT NULL REFERENCES bots(bot_user_id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    menu_button_type SMALLINT NOT NULL,
    menu_button_text VARCHAR(64) NOT NULL DEFAULT '',
    menu_button_url VARCHAR(512) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (bot_user_id, user_id),
    CONSTRAINT bot_user_menu_buttons_not_self CHECK (bot_user_id <> user_id),
    CONSTRAINT bot_user_menu_buttons_not_default CHECK (menu_button_type <> 0)
);

CREATE INDEX bot_user_menu_buttons_user_idx ON bot_user_menu_buttons (user_id);

-- 专属按钮不 bump bots 共享的 bot_info_version（那会经 user_base/bot_full 事件让所有实例
-- 失效所有人的机器人资料）。这里只对 (user, bot) 这一对发 dialog_light 事件：
-- ReadModelChangeListener 收到后调用 InvalidateRPCProjectionReadModelForPeer(user, bot)，
-- 精确失效该用户看该机器人的 userFull 投影（跨实例）。
CREATE FUNCTION public.telesrv_notify_bot_user_menu_button() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM telesrv_bump_dialog_light(OLD.user_id, 'user', OLD.bot_user_id);
    ELSE
        PERFORM telesrv_bump_dialog_light(NEW.user_id, 'user', NEW.bot_user_id);
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER bot_user_menu_buttons_read_model_changed
    AFTER INSERT OR UPDATE OR DELETE ON public.bot_user_menu_buttons
    FOR EACH ROW
    EXECUTE FUNCTION public.telesrv_notify_bot_user_menu_button();
