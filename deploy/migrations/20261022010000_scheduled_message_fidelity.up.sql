-- 定时消息到点发出要还原完整转发头：频道帖子的 channel_post（客户端据此跳回原帖），以及
-- 转发进收藏夹时的 saved_from 来源（收藏夹按来源会话分组）。此前这两项不落表，定时转发
-- 发出后转发头只剩 from/date。
ALTER TABLE public.scheduled_messages
    ADD COLUMN fwd_channel_post integer DEFAULT 0 NOT NULL,
    ADD COLUMN fwd_saved_from_peer_type character varying(16) DEFAULT ''::character varying NOT NULL,
    ADD COLUMN fwd_saved_from_peer_id bigint DEFAULT 0 NOT NULL,
    ADD COLUMN fwd_saved_from_msg_id integer DEFAULT 0 NOT NULL,
    ADD CONSTRAINT scheduled_messages_fwd_saved_from_peer_type_check CHECK ((((fwd_saved_from_peer_type)::text = ''::text) OR ((fwd_saved_from_peer_type)::text = ANY (ARRAY[('user'::character varying)::text, ('channel'::character varying)::text]))));
