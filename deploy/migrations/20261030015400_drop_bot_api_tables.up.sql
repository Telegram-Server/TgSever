-- Bot API 改由外部的 telegram-bot-api 经 MTProto 提供（#142/#154）：服务端自研网关的
-- getUpdates 队列、轮询状态和 webhook 配置都不再有读写方，整体删除。
DROP TABLE IF EXISTS public.bot_api_webhooks;
DROP TABLE IF EXISTS public.bot_api_update_states;
DROP TABLE IF EXISTS public.bot_api_updates;
